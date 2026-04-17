package connectors

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	sessionspec "github.com/mimecast/dtail/internal/session"

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
		sessionspec.Spec{},
		false,
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
		sessionspec.Spec{},
		false,
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

func TestServerConnectionSupportsQueryUpdates(t *testing.T) {
	resetClientLogger(t)

	conn := &ServerConnection{
		handler: &mockHandler{
			waitForCapabilities: true,
			capabilities: map[string]bool{
				protocol.CapabilityQueryUpdateV1: true,
			},
		},
	}

	if !conn.SupportsQueryUpdates(10 * time.Millisecond) {
		t.Fatalf("expected query-update capability to be detected")
	}
}

func TestServerConnectionSupportsQueryUpdatesFallsBackForOlderServers(t *testing.T) {
	resetClientLogger(t)

	conn := &ServerConnection{
		handler: &mockHandler{},
	}

	if conn.SupportsQueryUpdates(5 * time.Millisecond) {
		t.Fatalf("expected old-server fallback when no capability is advertised")
	}
}

func TestServerConnectionSupportsQueryUpdatesRequiresCapabilityFlag(t *testing.T) {
	resetClientLogger(t)

	conn := &ServerConnection{
		handler: &mockHandler{
			waitForCapabilities: true,
		},
	}

	if conn.SupportsQueryUpdates(10 * time.Millisecond) {
		t.Fatalf("expected capability wait success alone to be insufficient")
	}
}

func TestServerConnectionApplySessionSpecStart(t *testing.T) {
	resetClientLogger(t)

	conn := &ServerConnection{
		server: "srv1",
		handler: &mockHandler{
			waitForCapabilities: true,
			capabilities: map[string]bool{
				protocol.CapabilityQueryUpdateV1: true,
			},
			sessionAcks: []handlers.SessionAck{{
				Action:     "start",
				Generation: 1,
			}},
		},
	}

	spec := sessionspec.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}
	if err := conn.ApplySessionSpec(spec, 10*time.Millisecond); err != nil {
		t.Fatalf("ApplySessionSpec() error = %v", err)
	}

	mock := conn.handler.(*mockHandler)
	if len(mock.commands) != 1 {
		t.Fatalf("expected one session command, got %d", len(mock.commands))
	}
	if committedSpec, generation, ok := conn.CommittedSession(); !ok || generation != 1 || committedSpec.Regex != "ERROR" {
		t.Fatalf("unexpected committed session: spec=%#v generation=%d ok=%v", committedSpec, generation, ok)
	}
}

func TestServerConnectionApplySessionSpecUpdateUsesNextGeneration(t *testing.T) {
	resetClientLogger(t)

	mock := &mockHandler{
		waitForCapabilities: true,
		capabilities: map[string]bool{
			protocol.CapabilityQueryUpdateV1: true,
		},
		sessionAcks: []handlers.SessionAck{
			{Action: "start", Generation: 4},
			{Action: "update", Generation: 5},
		},
	}
	conn := &ServerConnection{
		server:  "srv1",
		handler: mock,
	}

	startSpec := sessionspec.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}
	updateSpec := sessionspec.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "WARN",
	}

	if err := conn.ApplySessionSpec(startSpec, 10*time.Millisecond); err != nil {
		t.Fatalf("start ApplySessionSpec() error = %v", err)
	}
	if err := conn.ApplySessionSpec(updateSpec, 10*time.Millisecond); err != nil {
		t.Fatalf("update ApplySessionSpec() error = %v", err)
	}
	if len(mock.commands) != 2 {
		t.Fatalf("expected two session commands, got %d", len(mock.commands))
	}
	if committedSpec, generation, ok := conn.CommittedSession(); !ok || generation != 5 || committedSpec.Regex != "WARN" {
		t.Fatalf("unexpected committed session after update: spec=%#v generation=%d ok=%v", committedSpec, generation, ok)
	}
}

func TestServerConnectionApplySessionSpecFallsBackForUnsupportedServer(t *testing.T) {
	resetClientLogger(t)

	conn := &ServerConnection{
		handler: &mockHandler{},
	}

	err := conn.ApplySessionSpec(sessionspec.Spec{Mode: omode.TailClient, Regex: "ERROR"}, 5*time.Millisecond)
	if !errors.Is(err, ErrSessionUnsupported) {
		t.Fatalf("expected ErrSessionUnsupported, got %v", err)
	}
}

func TestServerConnectionApplySessionSpecPreservesCommittedStateOnRejectedUpdate(t *testing.T) {
	resetClientLogger(t)

	mock := &mockHandler{
		waitForCapabilities: true,
		capabilities: map[string]bool{
			protocol.CapabilityQueryUpdateV1: true,
		},
		sessionAcks: []handlers.SessionAck{
			{Action: "start", Generation: 2},
			{Action: "error", Error: "bad reload"},
		},
	}
	conn := &ServerConnection{
		server:  "srv1",
		handler: mock,
	}

	startSpec := sessionspec.Spec{Mode: omode.TailClient, Regex: "ERROR"}
	if err := conn.ApplySessionSpec(startSpec, 10*time.Millisecond); err != nil {
		t.Fatalf("start ApplySessionSpec() error = %v", err)
	}

	err := conn.ApplySessionSpec(sessionspec.Spec{Mode: omode.TailClient, Regex: "WARN"}, 10*time.Millisecond)
	if !errors.Is(err, ErrSessionRejected) {
		t.Fatalf("expected ErrSessionRejected, got %v", err)
	}
	if committedSpec, generation, ok := conn.CommittedSession(); !ok || generation != 2 || committedSpec.Regex != "ERROR" {
		t.Fatalf("unexpected committed session after rejected update: spec=%#v generation=%d ok=%v", committedSpec, generation, ok)
	}
}

func TestServerConnectionApplySessionSpecRejectsUnexpectedAck(t *testing.T) {
	resetClientLogger(t)

	mock := &mockHandler{
		waitForCapabilities: true,
		capabilities: map[string]bool{
			protocol.CapabilityQueryUpdateV1: true,
		},
		sessionAcks: []handlers.SessionAck{
			{Action: "update", Generation: 1},
		},
	}
	conn := &ServerConnection{
		server:  "srv1",
		handler: mock,
	}

	err := conn.ApplySessionSpec(sessionspec.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}, 10*time.Millisecond)
	if !errors.Is(err, ErrUnexpectedSessionAck) {
		t.Fatalf("expected ErrUnexpectedSessionAck, got %v", err)
	}
	if _, _, ok := conn.CommittedSession(); ok {
		t.Fatalf("unexpected committed session after mismatched ack")
	}
}

func TestServerConnectionApplySessionSpecTimesOutWaitingForAck(t *testing.T) {
	resetClientLogger(t)

	mock := &mockHandler{
		waitForCapabilities: true,
		capabilities: map[string]bool{
			protocol.CapabilityQueryUpdateV1: true,
		},
	}
	conn := &ServerConnection{
		server:  "srv1",
		handler: mock,
	}

	err := conn.ApplySessionSpec(sessionspec.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}, 10*time.Millisecond)
	if !errors.Is(err, ErrSessionAckTimeout) {
		t.Fatalf("expected ErrSessionAckTimeout, got %v", err)
	}
	if len(mock.commands) != 1 {
		t.Fatalf("expected session command to be sent before timeout, got %d", len(mock.commands))
	}
	if _, _, ok := conn.CommittedSession(); ok {
		t.Fatalf("unexpected committed session after missing ack")
	}
}

func TestApplySessionSpecSerializesConcurrentBootstrapAndReload(t *testing.T) {
	resetClientLogger(t)

	handler := newBlockingSessionHandler()
	state := &committedSessionState{}

	initialSpec := sessionspec.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}
	reloadSpec := sessionspec.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "WARN",
	}

	initialErrCh := make(chan error, 1)
	go func() {
		initialErrCh <- dispatchInitialCommands("srv1", handler, nil, true, initialSpec, state)
	}()

	firstCommand := <-handler.commandsCh
	if !strings.HasPrefix(firstCommand, "SESSION START ") {
		t.Fatalf("expected initial SESSION START command, got %q", firstCommand)
	}

	reloadErrCh := make(chan error, 1)
	go func() {
		reloadErrCh <- applySessionSpec("srv1", handler, state, reloadSpec, 50*time.Millisecond)
	}()

	select {
	case command := <-handler.commandsCh:
		t.Fatalf("unexpected concurrent session command before bootstrap ack: %q", command)
	case <-time.After(10 * time.Millisecond):
	}

	handler.ackCh <- handlers.SessionAck{Action: "start", Generation: 1}
	if err := <-initialErrCh; err != nil {
		t.Fatalf("dispatchInitialCommands() error = %v", err)
	}

	secondCommand := <-handler.commandsCh
	if !strings.HasPrefix(secondCommand, "SESSION UPDATE 2 ") {
		t.Fatalf("expected reload to send SESSION UPDATE after bootstrap, got %q", secondCommand)
	}

	handler.ackCh <- handlers.SessionAck{Action: "update", Generation: 2}
	if err := <-reloadErrCh; err != nil {
		t.Fatalf("applySessionSpec() error = %v", err)
	}

	committedSpec, generation, ok := state.snapshot()
	if !ok || generation != 2 || committedSpec.Regex != "WARN" {
		t.Fatalf("unexpected committed session after reload: spec=%#v generation=%d ok=%v", committedSpec, generation, ok)
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

func (testHostKeyCallback) Wrap(context.Context) ssh.HostKeyCallback {
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
	commands            []string
	capabilities        map[string]bool
	waitForCapabilities bool
	sessionAcks         []handlers.SessionAck
}

var _ handlers.Handler = (*mockHandler)(nil)

func (m *mockHandler) SendMessage(command string) error {
	m.commands = append(m.commands, command)
	return nil
}

func (m *mockHandler) Capabilities() []string {
	var capabilities []string
	for capability := range m.capabilities {
		capabilities = append(capabilities, capability)
	}
	return capabilities
}

func (m *mockHandler) HasCapability(name string) bool {
	return m.capabilities[name]
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

func (m *mockHandler) WaitForCapabilities(timeout time.Duration) bool {
	return m.waitForCapabilities
}

func (m *mockHandler) WaitForSessionAck(timeout time.Duration) (handlers.SessionAck, bool) {
	if timeout <= 0 {
		return handlers.SessionAck{}, false
	}
	if len(m.sessionAcks) == 0 {
		return handlers.SessionAck{}, false
	}

	ack := m.sessionAcks[0]
	m.sessionAcks = m.sessionAcks[1:]
	return ack, true
}

func (m *mockHandler) Read(_ []byte) (int, error) {
	return 0, nil
}

func (m *mockHandler) Write(p []byte) (int, error) {
	return len(p), nil
}

type blockingSessionHandler struct {
	mu           sync.Mutex
	commands     []string
	commandsCh   chan string
	ackCh        chan handlers.SessionAck
	capabilities map[string]bool
}

func newBlockingSessionHandler() *blockingSessionHandler {
	return &blockingSessionHandler{
		commandsCh: make(chan string, 8),
		ackCh:      make(chan handlers.SessionAck, 8),
		capabilities: map[string]bool{
			protocol.CapabilityQueryUpdateV1: true,
		},
	}
}

var _ handlers.Handler = (*blockingSessionHandler)(nil)

func (h *blockingSessionHandler) SendMessage(command string) error {
	h.mu.Lock()
	h.commands = append(h.commands, command)
	h.mu.Unlock()
	h.commandsCh <- command
	return nil
}

func (h *blockingSessionHandler) Capabilities() []string {
	capabilities := make([]string, 0, len(h.capabilities))
	for capability := range h.capabilities {
		capabilities = append(capabilities, capability)
	}
	return capabilities
}

func (h *blockingSessionHandler) HasCapability(name string) bool {
	return h.capabilities[name]
}

func (*blockingSessionHandler) Server() string {
	return "mock"
}

func (*blockingSessionHandler) Status() int {
	return 0
}

func (*blockingSessionHandler) Shutdown() {}

func (*blockingSessionHandler) Done() <-chan struct{} {
	return make(chan struct{})
}

func (*blockingSessionHandler) WaitForCapabilities(time.Duration) bool {
	return true
}

func (h *blockingSessionHandler) WaitForSessionAck(timeout time.Duration) (handlers.SessionAck, bool) {
	if timeout <= 0 {
		select {
		case ack := <-h.ackCh:
			return ack, true
		default:
			return handlers.SessionAck{}, false
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case ack := <-h.ackCh:
		return ack, true
	case <-timer.C:
		return handlers.SessionAck{}, false
	}
}

func (*blockingSessionHandler) Read(_ []byte) (int, error) {
	return 0, nil
}

func (*blockingSessionHandler) Write(p []byte) (int, error) {
	return len(p), nil
}
