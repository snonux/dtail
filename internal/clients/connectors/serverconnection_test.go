package connectors

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/io/dlog"
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
