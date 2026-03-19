package client

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func testKnownHostsCallback(t *testing.T, knownHostsPath string) *KnownHostsCallback {
	t.Helper()

	throttleCh := make(chan struct{}, 1)
	throttleCh <- struct{}{}

	callback, err := NewKnownHostsCallback(knownHostsPath, false, throttleCh)
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
