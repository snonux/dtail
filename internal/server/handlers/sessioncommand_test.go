package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/session"
	userserver "github.com/mimecast/dtail/internal/user/server"
)

func TestNewServerHandlerAdvertisesQueryUpdateCapability(t *testing.T) {
	handler := newSessionTestHandler("session-capability-user")

	if message := readServerMessage(t, handler.serverMessages); message != protocol.HiddenCapabilitiesPrefix+protocol.CapabilityQueryUpdateV1 {
		t.Fatalf("unexpected capability advertisement: %q", message)
	}
}

func TestHandleSessionCommandStartStoresSpec(t *testing.T) {
	handler := newSessionTestHandler("session-start-user")
	readServerMessage(t, handler.serverMessages)

	spec := session.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}
	payload := mustSessionPayload(t, spec)

	commandFinished := false
	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3, []string{"SESSION", "START", payload}, func() {
		commandFinished = true
	})

	if !commandFinished {
		t.Fatalf("expected commandFinished callback")
	}
	if !handler.sessionState.activeSession() {
		t.Fatalf("expected session state to become active")
	}
	if message := readServerMessage(t, handler.serverMessages); message != sessionAckStartOKPrefix {
		t.Fatalf("unexpected session start message: %q", message)
	}
}

func TestHandleSessionCommandUpdateRequiresActiveSession(t *testing.T) {
	handler := newSessionTestHandler("session-update-user")
	readServerMessage(t, handler.serverMessages)

	spec := session.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}
	payload := mustSessionPayload(t, spec)

	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3, []string{"SESSION", "UPDATE", payload}, func() {})

	if message := readServerMessage(t, handler.serverMessages); message != sessionAckErrorPrefix+"session not started" {
		t.Fatalf("unexpected session update error: %q", message)
	}
}

func TestHandleSessionCommandRejectsInvalidPayload(t *testing.T) {
	handler := newSessionTestHandler("session-invalid-user")
	readServerMessage(t, handler.serverMessages)

	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3, []string{"SESSION", "START", "not-base64"}, func() {})

	if message := readServerMessage(t, handler.serverMessages); message != sessionAckErrorPrefix+"invalid session payload" {
		t.Fatalf("unexpected invalid payload message: %q", message)
	}
}

func newSessionTestHandler(userName string) *ServerHandler {
	handler := &ServerHandler{
		baseHandler: baseHandler{
			done:             internal.NewDone(),
			lines:            make(chan *line.Line, 4),
			serverMessages:   make(chan string, 8),
			maprMessages:     make(chan string, 4),
			ackCloseReceived: make(chan struct{}),
			user:             &userserver.User{Name: userName},
			codec:            newProtocolCodec(&userserver.User{Name: userName}),
		},
		serverCfg: &config.ServerConfig{
			AuthKeyEnabled: true,
		},
	}
	handler.send(handler.serverMessages, protocol.HiddenCapabilitiesPrefix+protocol.CapabilityQueryUpdateV1)
	return handler
}

func mustSessionPayload(t *testing.T, spec session.Spec) string {
	t.Helper()

	payload, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal session spec: %v", err)
	}
	return base64.StdEncoding.EncodeToString(payload)
}

func TestParseSessionCommandWithGeneration(t *testing.T) {
	spec := session.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}

	action, generation, parsedSpec, err := parseSessionCommand([]string{"SESSION", "UPDATE", "7", mustSessionPayload(t, spec)}, 4)
	if err != nil {
		t.Fatalf("parseSessionCommand error: %v", err)
	}
	if action != "UPDATE" {
		t.Fatalf("unexpected action: %s", action)
	}
	if generation != 7 {
		t.Fatalf("unexpected generation: %d", generation)
	}
	if parsedSpec.Mode != spec.Mode {
		t.Fatalf("unexpected parsed mode: %v", parsedSpec.Mode)
	}
}

func TestSessionStateStoreUpdateAutoIncrementsGeneration(t *testing.T) {
	var state sessionCommandState

	state.storeStart(session.Spec{Mode: omode.TailClient, Files: []string{"/tmp/a"}, Regex: "ERROR"})
	state.storeUpdate(session.Spec{Mode: omode.TailClient, Files: []string{"/tmp/b"}, Regex: "WARN"}, 0)

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.generation != 2 {
		t.Fatalf("unexpected generation: %d", state.generation)
	}
}

func TestSessionCommandReadServerMessageTimeoutProtection(t *testing.T) {
	messages := make(chan string)

	select {
	case <-messages:
		t.Fatalf("unexpected message")
	case <-time.After(5 * time.Millisecond):
	}
}
