package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/knownhosts"
)

// trustAllMaxWait bounds how long a trust-all handshake may wait for its
// unknown host to be trusted. It is far above trustAllHostsBatchDelay, to stay
// robust on a loaded machine, and far below unknownHostsPromptDelay, so a
// regression to the interactive debounce fails the test.
const trustAllMaxWait = time.Second

func TestBatchTimerDelay(t *testing.T) {
	tests := []struct {
		name      string
		trustAll  bool
		pending   int
		wantDelay time.Duration
		wantArm   bool
	}{
		{"interactive first host", false, 1, unknownHostsPromptDelay, true},
		{"interactive later host restarts", false, 2, unknownHostsPromptDelay, true},
		{"trust-all first host", true, 1, trustAllHostsBatchDelay, true},
		{"trust-all later host keeps window", true, 2, trustAllHostsBatchDelay, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			callback := newBatchTestCallback(t, test.trustAll)
			delay, arm := callback.batchTimerDelay(test.pending)
			if delay != test.wantDelay || arm != test.wantArm {
				t.Fatalf("batchTimerDelay(%d) = (%v, %v), want (%v, %v)",
					test.pending, delay, arm, test.wantDelay, test.wantArm)
			}
		})
	}
}

// TestPromptAddHostsTrustAllAnswersQuickly verifies that in trust-all mode an
// unknown host is trusted (and written to known_hosts) without waiting for
// the 2 s interactive prompt debounce, for one host and for several hosts
// arriving at the same time.
func TestPromptAddHostsTrustAllAnswersQuickly(t *testing.T) {
	for _, hosts := range []int{1, 3} {
		t.Run(fmt.Sprintf("%d hosts", hosts), func(t *testing.T) {
			callback := newBatchTestCallback(t, true)
			ctx, stop := startPromptAddHosts(t, callback)
			defer stop()

			start := time.Now()
			errCh := make(chan error, hosts)
			for i := range hosts {
				go func() { errCh <- callUnknownHost(ctx, callback, i) }()
			}
			for range hosts {
				select {
				case err := <-errCh:
					if err != nil {
						t.Fatalf("trust-all host key callback failed: %v", err)
					}
				case <-time.After(trustAllMaxWait):
					t.Fatalf("trust-all host key callback did not return within %v", trustAllMaxWait)
				}
			}
			t.Logf("%d unknown hosts trusted after %v", hosts, time.Since(start))
			assertKnownHostsLines(t, callback.knownHostsPath, batchHostLines(hosts))
		})
	}
}

// TestPromptAddHostsInteractiveKeepsDebounce is the negative counterpart:
// without trust-all mode the unknown host still waits for the interactive
// debounce, and cancelling ctx during that wait aborts the handshake callback
// and stops PromptAddHosts.
func TestPromptAddHostsInteractiveKeepsDebounce(t *testing.T) {
	callback := newBatchTestCallback(t, false)
	ctx, stop := startPromptAddHosts(t, callback)

	errCh := make(chan error, 1)
	go func() { errCh <- callUnknownHost(ctx, callback, 0) }()

	select {
	case err := <-errCh:
		t.Fatalf("interactive host key callback returned before the prompt debounce: %v", err)
	case <-time.After(unknownHostsPromptDelay / 4):
	}
	stop()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled after cancel, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("interactive host key callback did not return after ctx cancel")
	}
	assertKnownHostsEmpty(t, callback.knownHostsPath)
}

func newBatchTestCallback(t *testing.T, trustAll bool) *KnownHostsCallback {
	t.Helper()
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHostsPath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	callback, err := NewKnownHostsCallback(knownHostsPath, trustAll,
		sshClientTestLogger, sshClientTestLogger)
	if err != nil {
		t.Fatalf("NewKnownHostsCallback failed: %v", err)
	}
	return callback.(*KnownHostsCallback)
}

// startPromptAddHosts runs PromptAddHosts until the returned stop function
// is called; stop cancels the context and waits for PromptAddHosts to return.
func startPromptAddHosts(t *testing.T, callback *KnownHostsCallback) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		callback.PromptAddHosts(ctx)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("PromptAddHosts did not return after ctx cancel")
		}
	}
	return ctx, stop
}

// callUnknownHost runs the wrapped host key callback for host number i, with
// a server name, address and key that no other host number uses.
func callUnknownHost(ctx context.Context, callback *KnownHostsCallback, i int) error {
	server, remote, key := batchHost(i)
	return callback.Wrap(ctx)(server, remote, key)
}

func batchHost(i int) (string, *net.TCPAddr, *mockPublicKey) {
	return fmt.Sprintf("batch%d.example:2222", i),
		testTCPAddr(fmt.Sprintf("127.0.0.1:%d", 2300+i)),
		&mockPublicKey{id: fmt.Sprintf("batch%d", i)}
}

// batchHostLines returns the known_hosts host and IP lines of the first
// hosts host numbers of callUnknownHost.
func batchHostLines(hosts int) []string {
	var lines []string
	for i := range hosts {
		server, remote, key := batchHost(i)
		lines = append(lines, knownhosts.Line([]string{server}, key),
			knownhosts.Line([]string{remote.String()}, key))
	}
	return lines
}

// assertKnownHostsEmpty checks that no host was added to known_hosts.
func assertKnownHostsEmpty(t *testing.T, knownHostsPath string) {
	t.Helper()
	data, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("known_hosts is not empty:\n%s", data)
	}
}
