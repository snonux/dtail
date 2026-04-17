package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"

	"golang.org/x/crypto/ssh/knownhosts"
)

func TestTrustHostsAppendsDistinctExistingEntries(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	existingLine := knownhosts.Line([]string{"old.example:2222"}, &mockPublicKey{id: "old"})
	if err := os.WriteFile(knownHostsPath, []byte(existingLine+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	callback := testKnownHostsCallback(t, knownHostsPath)
	unknown := testUnknownHost("new.example:2222", "127.0.0.1:2222", "new")

	if err := callback.trustHosts([]unknownHost{unknown}); err != nil {
		t.Fatalf("trustHosts failed: %v", err)
	}

	got, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	want := strings.Join([]string{
		unknown.hostLine,
		unknown.ipLine,
		existingLine,
		"",
	}, "\n")
	if string(got) != want {
		t.Fatalf("trustHosts wrote:\n%s\nwant:\n%s", got, want)
	}

	if response := <-unknown.responseCh; response != trustHost {
		t.Fatalf("unexpected trust response: %v", response)
	}
}

func TestTrustHostsReplacesExistingEntriesForSameHostAndIP(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	oldUnknown := testUnknownHost("replace.example:2222", "127.0.0.1:2222", "old")
	keepLine := knownhosts.Line([]string{"keep.example:2222"}, &mockPublicKey{id: "keep"})
	initialContents := strings.Join([]string{
		oldUnknown.hostLine,
		oldUnknown.ipLine,
		keepLine,
		"",
	}, "\n")
	if err := os.WriteFile(knownHostsPath, []byte(initialContents), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	callback := testKnownHostsCallback(t, knownHostsPath)
	newUnknown := testUnknownHost("replace.example:2222", "127.0.0.1:2222", "new")

	if err := callback.trustHosts([]unknownHost{newUnknown}); err != nil {
		t.Fatalf("trustHosts failed: %v", err)
	}

	got, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	want := strings.Join([]string{
		newUnknown.hostLine,
		newUnknown.ipLine,
		keepLine,
		"",
	}, "\n")
	if string(got) != want {
		t.Fatalf("trustHosts wrote:\n%s\nwant:\n%s", got, want)
	}

	if response := <-newUnknown.responseCh; response != trustHost {
		t.Fatalf("unexpected trust response: %v", response)
	}
}

func TestTrustHostsRejectsEscapingKnownHostsSymlink(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "ssh")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	outsidePath := filepath.Join(filepath.Dir(rootDir), "outside_known_hosts")
	if err := os.WriteFile(outsidePath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	knownHostsPath := filepath.Join(rootDir, "known_hosts")
	if err := os.Symlink(filepath.Join("..", "outside_known_hosts"), knownHostsPath); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	callback := testKnownHostsCallback(t, knownHostsPath)
	unknown := testUnknownHost("escape.example:2222", "127.0.0.1:2222", "new")

	if err := callback.trustHosts([]unknownHost{unknown}); err == nil {
		t.Fatalf("trustHosts succeeded for escaping known_hosts symlink")
	}
}

// stubClientLogger installs a no-op dlog.Client for tests that exercise the
// host-key callback (which emits a Debug log on the unknown-host path).
func stubClientLogger(t *testing.T) {
	t.Helper()
	original := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() { dlog.Client = original })
}

// TestWrapReturnsWhenCtxCancelledBeforeUnknownChSend verifies that when
// PromptAddHosts has already exited (no consumer on unknownCh), the wrapped
// host-key callback unblocks on ctx cancel instead of hanging the SSH
// handshake and leaking a goroutine. Pre-fix this test times out because
// `c.unknownCh <- unknown` blocks forever.
func TestWrapReturnsWhenCtxCancelledBeforeUnknownChSend(t *testing.T) {
	stubClientLogger(t)
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHostsPath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	callback := testKnownHostsCallback(t, knownHostsPath)
	ctx, cancel := context.WithCancel(context.Background())

	wrapped := callback.Wrap(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- wrapped("host.example:2222", testTCPAddr("127.0.0.1:2222"),
			&mockPublicKey{id: "new"})
	}()

	// Give the goroutine a moment to park on the unknownCh send, then cancel.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("expected non-nil error after ctx cancel, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected error to wrap context.Canceled, got %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("host key callback did not return within 100ms after ctx cancel")
	}
}

// TestWrapReturnsWhenCtxCancelledBeforeResponse verifies that when a consumer
// has picked the unknown host off unknownCh but never writes a response
// (e.g. PromptAddHosts was cancelled mid-batch), the callback still unblocks
// on ctx cancel rather than blocking on responseCh forever.
func TestWrapReturnsWhenCtxCancelledBeforeResponse(t *testing.T) {
	stubClientLogger(t)
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHostsPath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	callback := testKnownHostsCallback(t, knownHostsPath)
	ctx, cancel := context.WithCancel(context.Background())

	// Simulate a consumer that drains unknownCh but never writes to
	// responseCh, mimicking PromptAddHosts buffering a batch and then exiting.
	consumed := make(chan struct{})
	go func() {
		<-callback.unknownCh
		close(consumed)
	}()

	wrapped := callback.Wrap(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- wrapped("host.example:2222", testTCPAddr("127.0.0.1:2222"),
			&mockPublicKey{id: "new"})
	}()

	select {
	case <-consumed:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("consumer never received unknown host")
	}

	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("expected non-nil error after ctx cancel, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected error to wrap context.Canceled, got %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("host key callback did not return within 100ms after ctx cancel")
	}
}

func testKnownHostsCallback(t *testing.T, knownHostsPath string) *KnownHostsCallback {
	t.Helper()

	callback, err := NewKnownHostsCallback(knownHostsPath, false)
	if err != nil {
		t.Fatalf("NewKnownHostsCallback failed: %v", err)
	}

	knownHostsCallback, ok := callback.(*KnownHostsCallback)
	if !ok {
		t.Fatalf("unexpected callback type %T", callback)
	}

	return knownHostsCallback
}

func testUnknownHost(server, remoteAddr, keyID string) unknownHost {
	key := &mockPublicKey{id: keyID}
	remote := testTCPAddr(remoteAddr)

	return unknownHost{
		server:     server,
		remote:     remote,
		key:        key,
		hostLine:   knownhosts.Line([]string{server}, key),
		ipLine:     knownhosts.Line([]string{remote.String()}, key),
		responseCh: make(chan response, 1),
	}
}

func testTCPAddr(address string) *net.TCPAddr {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		panic(err)
	}

	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		panic(err)
	}

	return &net.TCPAddr{IP: net.ParseIP(host), Port: port}
}
