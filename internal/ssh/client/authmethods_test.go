package client

import (
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/mimecast/dtail/internal/io/dlog"

	gossh "golang.org/x/crypto/ssh"
)

// testCloser is a sentinel io.Closer used by tests to assert that callers
// release ssh-agent connections returned by the mocked agentSigners hook.
type testCloser struct {
	closed int
}

func (c *testCloser) Close() error {
	c.closed++
	return nil
}

type mockPublicKey struct {
	id string
}

func (k *mockPublicKey) Type() string {
	return "ssh-rsa"
}

func (k *mockPublicKey) Marshal() []byte {
	return []byte(k.id)
}

func (k *mockPublicKey) Verify(_ []byte, _ *gossh.Signature) error {
	return nil
}

type mockSigner struct {
	key gossh.PublicKey
}

func newMockSigner(id string) gossh.Signer {
	return &mockSigner{key: &mockPublicKey{id: id}}
}

func (s *mockSigner) PublicKey() gossh.PublicKey {
	return s.key
}

func (s *mockSigner) Sign(_ io.Reader, _ []byte) (*gossh.Signature, error) {
	return &gossh.Signature{
		Format: "ssh-rsa",
		Blob:   []byte("sig"),
	}, nil
}

func TestCollectKnownHostsAuthMethodsOrder(t *testing.T) {
	homeDir := "/tmp/dtail-auth-order"
	t.Setenv("HOME", homeDir)
	// Keep this unit test deterministic regardless of integration-mode env.
	t.Setenv("DTAIL_INTEGRATION_TEST_RUN_MODE", "")

	originalPrivateKeySigner := privateKeySigner
	originalAgentSigners := agentSigners
	originalLogger := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() {
		privateKeySigner = originalPrivateKeySigner
		agentSigners = originalAgentSigners
		dlog.Client = originalLogger
	})

	var callOrder []string
	successfulPrivateKeys := map[string]gossh.Signer{
		"/custom/id_fast":        newMockSigner("custom"),
		homeDir + "/.ssh/id_rsa": newMockSigner("default-rsa"),
		homeDir + "/.ssh/id_dsa": newMockSigner("default-dsa"),
	}

	privateKeySigner = func(path string) (gossh.Signer, error) {
		callOrder = append(callOrder, "private:"+path)
		signer, found := successfulPrivateKeys[path]
		if !found {
			return nil, fmt.Errorf("missing private key: %s", path)
		}
		return signer, nil
	}
	agentCloser := &testCloser{}
	agentSigners = func(keyIndex int) ([]gossh.Signer, io.Closer, error) {
		callOrder = append(callOrder, fmt.Sprintf("agent:%d", keyIndex))
		return []gossh.Signer{newMockSigner("agent")}, agentCloser, nil
	}

	methods, closer := collectKnownHostsAuthMethods("/custom/id_fast", 7)
	if len(methods) != 1 {
		t.Fatalf("Expected 1 auth method, got %d", len(methods))
	}
	if closer == nil {
		t.Fatalf("Expected non-nil agent closer from collectKnownHostsAuthMethods")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("agent closer returned error: %v", err)
	}
	if agentCloser.closed < 1 {
		t.Fatalf("Expected caller to be able to close agent conn; closed=%d", agentCloser.closed)
	}

	callOrder = nil
	signers, sCloser := collectKnownHostsSigners("/custom/id_fast", 7)
	if len(signers) != 4 {
		t.Fatalf("Expected 4 signers, got %d", len(signers))
	}
	if sCloser == nil {
		t.Fatalf("Expected non-nil agent closer from collectKnownHostsSigners")
	}
	_ = sCloser.Close()

	expectedOrder := []string{
		"private:/custom/id_fast",
		"agent:7",
		"private:/tmp/dtail-auth-order/.ssh/id_rsa",
		"private:/tmp/dtail-auth-order/.ssh/id_dsa",
		"private:/tmp/dtail-auth-order/.ssh/id_ecdsa",
		"private:/tmp/dtail-auth-order/.ssh/id_ed25519",
	}
	if !reflect.DeepEqual(callOrder, expectedOrder) {
		t.Fatalf("Unexpected auth method call order.\nexpected: %v\ngot:      %v", expectedOrder, callOrder)
	}
}

func TestCollectKnownHostsAuthMethodsSkipsDuplicateDefaultPath(t *testing.T) {
	homeDir := "/tmp/dtail-auth-dedupe"
	t.Setenv("HOME", homeDir)
	// Keep this unit test deterministic regardless of integration-mode env.
	t.Setenv("DTAIL_INTEGRATION_TEST_RUN_MODE", "")

	originalPrivateKeySigner := privateKeySigner
	originalAgentSigners := agentSigners
	originalLogger := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() {
		privateKeySigner = originalPrivateKeySigner
		agentSigners = originalAgentSigners
		dlog.Client = originalLogger
	})

	sharedSigner := newMockSigner("shared")
	var callOrder []string
	privateKeySigner = func(path string) (gossh.Signer, error) {
		callOrder = append(callOrder, "private:"+path)
		if path == homeDir+"/.ssh/id_rsa" {
			return sharedSigner, nil
		}
		return nil, fmt.Errorf("missing private key: %s", path)
	}
	agentCloser := &testCloser{}
	agentSigners = func(keyIndex int) ([]gossh.Signer, io.Closer, error) {
		callOrder = append(callOrder, fmt.Sprintf("agent:%d", keyIndex))
		return []gossh.Signer{sharedSigner}, agentCloser, nil
	}

	methods, closer := collectKnownHostsAuthMethods(homeDir+"/.ssh/id_rsa", 2)
	if len(methods) != 1 {
		t.Fatalf("Expected 1 auth method, got %d", len(methods))
	}
	if closer == nil {
		t.Fatalf("Expected non-nil agent closer from collectKnownHostsAuthMethods")
	}
	_ = closer.Close()

	callOrder = nil
	signers, sCloser := collectKnownHostsSigners(homeDir+"/.ssh/id_rsa", 2)
	if len(signers) != 1 {
		t.Fatalf("Expected duplicate keys to collapse to 1 signer, got %d", len(signers))
	}
	if sCloser == nil {
		t.Fatalf("Expected non-nil agent closer from collectKnownHostsSigners")
	}
	_ = sCloser.Close()

	expectedOrder := []string{
		"private:/tmp/dtail-auth-dedupe/.ssh/id_rsa",
		"agent:2",
		"private:/tmp/dtail-auth-dedupe/.ssh/id_dsa",
		"private:/tmp/dtail-auth-dedupe/.ssh/id_ecdsa",
		"private:/tmp/dtail-auth-dedupe/.ssh/id_ed25519",
	}
	if !reflect.DeepEqual(callOrder, expectedOrder) {
		t.Fatalf("Unexpected auth method call order.\nexpected: %v\ngot:      %v", expectedOrder, callOrder)
	}
}
