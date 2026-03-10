package connectors

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/io/dlog"

	"golang.org/x/crypto/ssh"
)

func TestExtractAuthKeyBase64(t *testing.T) {
	originalLogger := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Client = originalLogger
	})

	t.Run("valid authorized key line", func(t *testing.T) {
		pubKey := []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA user@host\n")

		got, err := extractAuthKeyBase64(pubKey)
		if err != nil {
			t.Fatalf("Expected valid key, got error: %v", err)
		}
		if got != "AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
			t.Fatalf("Unexpected base64 payload: %s", got)
		}
	})

	t.Run("invalid key format", func(t *testing.T) {
		_, err := extractAuthKeyBase64([]byte("not-a-valid-authorized-key-line"))
		if err == nil {
			t.Fatalf("Expected parse error for invalid key format")
		}
	})

	t.Run("invalid base64 payload", func(t *testing.T) {
		_, err := extractAuthKeyBase64([]byte("ssh-ed25519 !!! not-valid\n"))
		if err == nil {
			t.Fatalf("Expected error for invalid base64 payload")
		}
	})
}

func TestSendAuthKeyRegistrationCommand(t *testing.T) {
	originalLogger := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Client = originalLogger
	})

	tempDir := t.TempDir()
	privateKeyPath := filepath.Join(tempDir, "id_rsa")
	publicKeyPath := privateKeyPath + ".pub"
	if err := os.WriteFile(publicKeyPath,
		[]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA user@host\n"), 0600); err != nil {
		t.Fatalf("Unable to write public key test file: %v", err)
	}

	handler := &mockHandler{}
	conn := &ServerConnection{
		server:      "srv1",
		handler:     handler,
		authKeyPath: privateKeyPath,
	}

	conn.sendAuthKeyRegistrationCommand()

	if len(handler.commands) != 1 {
		t.Fatalf("Expected one AUTHKEY command, got %d", len(handler.commands))
	}
	expected := "AUTHKEY AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if handler.commands[0] != expected {
		t.Fatalf("Unexpected AUTHKEY command.\nexpected: %s\ngot:      %s", expected, handler.commands[0])
	}
}

func TestNewServerConnectionUsesInjectedSettings(t *testing.T) {
	resetClientLogger(t)

	conn := NewServerConnection(
		"srv1",
		"user",
		nil,
		testHostKeyCallback{},
		&mockHandler{},
		nil,
		"",
		false,
		testSSHSettings{port: 3022, timeout: 5 * time.Second},
	)

	if conn.hostname != "srv1" {
		t.Fatalf("Expected hostname srv1, got %q", conn.hostname)
	}
	if conn.port != 3022 {
		t.Fatalf("Expected injected port 3022, got %d", conn.port)
	}
	if conn.config.Timeout != 5*time.Second {
		t.Fatalf("Expected injected timeout 5s, got %v", conn.config.Timeout)
	}
}

func TestNewServerConnectionFallsBackToDefaults(t *testing.T) {
	resetClientLogger(t)

	conn := NewServerConnection(
		"srv1",
		"user",
		nil,
		testHostKeyCallback{},
		&mockHandler{},
		nil,
		"",
		false,
		testSSHSettings{},
	)

	if conn.port != defaultSSHPort {
		t.Fatalf("Expected default port %d, got %d", defaultSSHPort, conn.port)
	}
	if conn.config.Timeout != defaultSSHConnectTimeout {
		t.Fatalf("Expected default timeout %v, got %v", defaultSSHConnectTimeout, conn.config.Timeout)
	}
}

type testSSHSettings struct {
	port    int
	timeout time.Duration
}

func (s testSSHSettings) SSHPort() int {
	return s.port
}

func (s testSSHSettings) SSHConnectTimeout() time.Duration {
	return s.timeout
}

type testHostKeyCallback struct{}

func (testHostKeyCallback) Wrap() ssh.HostKeyCallback {
	return ssh.InsecureIgnoreHostKey()
}

func (testHostKeyCallback) Untrusted(string) bool {
	return false
}

func (testHostKeyCallback) PromptAddHosts(context.Context) {}

func resetClientLogger(t *testing.T) {
	t.Helper()

	originalLogger := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Client = originalLogger
	})
}

type mockHandler struct {
	commands []string
}

var _ handlers.Handler = (*mockHandler)(nil)

func (m *mockHandler) SendMessage(command string) error {
	m.commands = append(m.commands, command)
	return nil
}

func (m *mockHandler) Server() string {
	return "mock"
}

func (m *mockHandler) Status() int {
	return 0
}

func (m *mockHandler) Shutdown() {}

func (m *mockHandler) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (m *mockHandler) Read(_ []byte) (int, error) {
	return 0, nil
}

func (m *mockHandler) Write(p []byte) (int, error) {
	return len(p), nil
}
