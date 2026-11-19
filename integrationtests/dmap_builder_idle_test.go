package integrationtests

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mimecast/dtail/internal/clients/connectors"
)

// builderIdleConfig is the config of a dserver that closes idle sessions
// after 4 s (plus two to three refresh intervals of 1 s).
const builderIdleConfig = "dmap_builder_idle.cfg.tmp"

// builderKeepAliveRequest is the SSH request the query builder of dmap
// sends over the sessions it holds (connectors.ServerConnection.KeepAlive),
// by the client's own name for it. That the builder sends it over its held
// connections is covered by the client's tests (TestKeepSessionsAlive, the
// keepAliver assertion on ServerConnection and
// TestKeepAliveSendsARequestTheServerReads); this test covers that dserver
// takes it as activity.
const builderKeepAliveRequest = connectors.KeepAliveRequest

// dialTestServer opens an authenticated SSH connection to the dserver on
// port as the tests' user, with the suite's key, and closes it with the
// test.
func dialTestServer(t *testing.T, port int) *ssh.Client {
	t.Helper()
	keyPath := os.Getenv("DTAIL_AUTH_KEY_PATH")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read the suite's SSH key %q: %v", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatalf("parse the suite's SSH key: %v", err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", fmt.Sprintf("localhost:%d", port), &ssh.ClientConfig{
		User:            current.Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect to the test dserver: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// connectionClosed returns a channel closed when the server closed client.
func connectionClosed(client *ssh.Client) <-chan struct{} {
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		_ = client.Wait()
	}()
	return closed
}

// TestIdleSessionKeptByKeepAlive checks, against a real dserver with a
// short IdleSessionTimeoutS, what keeps the idle sessions of dmap's query
// builder from being closed while a query is built: a connection that only
// sends the builder's SSH keepalive request stays open well past the idle
// timeout, while one that sends nothing is closed by it. dserver needs no
// change for it: it counts every packet it reads as activity and discards
// the request, as it does with any request it does not know.
func TestIdleSessionKeptByKeepAlive(t *testing.T) {
	skipIfNotIntegrationTest(t)
	ctx, cancel := createTestContextWithTimeout(t)
	defer cancel()
	if err := os.WriteFile(builderIdleConfig, []byte(`{"Server":{"IdleSessionTimeoutS":4}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupFiles(t, builderIdleConfig)
	port := getUniquePortNumber()
	collector := &processOutputCollector{}
	launchInteractiveTestServer(ctx, t, "../dserver", []string{"--cfg", builderIdleConfig,
		"--logger", "stdout", "--logLevel", "debug", "--bindAddress", "localhost",
		"--port", fmt.Sprintf("%d", port)}, collector)
	if err := waitForServerReady(ctx, "localhost", port); err != nil {
		t.Fatalf("wait for dserver: %v", err)
	}

	kept, idle := dialTestServer(t, port), dialTestServer(t, port)
	keptClosed, idleClosed := connectionClosed(kept), connectionClosed(idle)
	// Three times the idle timeout, a keepalive every second.
	keepAliveUntil(ctx, t, kept, keptClosed, 12*time.Second)

	select {
	case <-idleClosed:
	default:
		t.Fatalf("the connection without keepalives outlived the idle timeout, server log:\n%s", collector.dump())
	}
	if _, _, err := kept.SendRequest(builderKeepAliveRequest, true, nil); err != nil {
		t.Fatalf("the kept connection does not answer any more: %v", err)
	}
}

// keepAliveUntil sends the builder's keepalive over client every second
// for the given time and fails if the server closes the connection
// meanwhile.
func keepAliveUntil(ctx context.Context, t *testing.T, client *ssh.Client, closed <-chan struct{}, d time.Duration) {
	t.Helper()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	end := time.After(d)
	for {
		select {
		case <-closed:
			t.Fatal("the server closed the connection although keepalives were sent")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-end:
			return
		case <-ticker.C:
			if _, _, err := client.SendRequest(builderKeepAliveRequest, false, nil); err != nil {
				t.Fatalf("send keepalive: %v", err)
			}
		}
	}
}
