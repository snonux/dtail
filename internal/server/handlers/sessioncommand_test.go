package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
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
	if message := readServerMessage(t, handler.serverMessages); message != sessionAckStartOKPrefix+" 1" {
		t.Fatalf("unexpected session start message: %q", message)
	}
}

func TestHandleSessionCommandUpdateCancelsPreviousGenerationImmediately(t *testing.T) {
	handler, recorder := newSessionDispatchTestHandler("session-update-cancel-user")
	readServerMessage(t, handler.serverMessages)
	t.Cleanup(func() {
		if handler.sessionState.cancel != nil {
			handler.sessionState.cancel()
		}
		recorder.wg.Wait()
	})

	startPayload := mustSessionPayload(t, session.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app-a.log"},
		Regex: "ERROR",
	})
	updatePayload := mustSessionPayload(t, session.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app-b.log"},
		Regex: "WARN",
	})

	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3, []string{"SESSION", "START", startPayload}, func() {})
	if message := readServerMessage(t, handler.serverMessages); message != sessionAckStartOKPrefix+" 1" {
		t.Fatalf("unexpected session start ack: %q", message)
	}

	first := recorder.waitForStart(t)
	if !strings.Contains(first.command, "/var/log/app-a.log") {
		t.Fatalf("expected first command to target app-a.log, got %q", first.command)
	}

	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3, []string{"SESSION", "UPDATE", updatePayload}, func() {})
	if message := readServerMessage(t, handler.serverMessages); message != sessionAckUpdateOKPrefix+" 2" {
		t.Fatalf("unexpected session update ack: %q", message)
	}

	waitForContextDone(t, first.ctx)

	second := recorder.waitForStart(t)
	if !strings.Contains(second.command, "/var/log/app-b.log") {
		t.Fatalf("expected second command to target app-b.log, got %q", second.command)
	}
	select {
	case <-second.ctx.Done():
		t.Fatalf("expected replacement generation context to remain active")
	default:
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

func TestHandleSessionCommandRejectsUnsupportedQuerySessions(t *testing.T) {
	handler := newSessionTestHandler("session-query-user")
	readServerMessage(t, handler.serverMessages)

	payload := mustSessionPayload(t, session.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Query: "from STATS select count(*)",
		Regex: ".",
	})

	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3, []string{"SESSION", "START", payload}, func() {})

	if message := readServerMessage(t, handler.serverMessages); message != sessionAckErrorPrefix+"query sessions not supported yet" {
		t.Fatalf("unexpected query-session error: %q", message)
	}
}

func TestHandleSessionCommandRejectsInvalidSerializedOptions(t *testing.T) {
	handler := newSessionTestHandler("session-options-user")
	readServerMessage(t, handler.serverMessages)

	payload := mustSessionPayload(t, session.Spec{
		Mode:    omode.TailClient,
		Files:   []string{"/var/log/app.log"},
		Options: "badoption",
		Regex:   "ERROR",
	})

	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3, []string{"SESSION", "START", payload}, func() {})

	if message := readServerMessage(t, handler.serverMessages); message != sessionAckErrorPrefix+"invalid session spec" {
		t.Fatalf("unexpected invalid options error: %q", message)
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
	handler.commands = map[string]commandHandler{
		"tail": immediateNoopCommandHandler,
		"cat":  immediateNoopCommandHandler,
		"grep": immediateNoopCommandHandler,
		"map":  immediateNoopCommandHandler,
	}
	handler.handleCommandCb = func(ctx context.Context, ltx lcontext.LContext, argc int, args []string, commandName string) {
		if command, found := handler.commands[commandName]; found {
			command(ctx, ltx, argc, args, func() {})
		}
	}
	handler.send(handler.serverMessages, protocol.HiddenCapabilitiesPrefix+protocol.CapabilityQueryUpdateV1)
	return handler
}

type recordedCommand struct {
	command string
	ctx     context.Context
}

type sessionDispatchRecorder struct {
	starts chan recordedCommand
	wg     sync.WaitGroup
}

func newSessionDispatchTestHandler(userName string) (*ServerHandler, *sessionDispatchRecorder) {
	handler := newSessionTestHandler(userName)
	recorder := &sessionDispatchRecorder{
		starts: make(chan recordedCommand, 4),
	}
	handler.commands = map[string]commandHandler{
		"tail": func(ctx context.Context, _ lcontext.LContext, argc int, args []string, commandFinished func()) {
			recorder.starts <- recordedCommand{
				command: strings.Join(args, " "),
				ctx:     ctx,
			}
			recorder.wg.Add(1)
			go func() {
				defer recorder.wg.Done()
				<-ctx.Done()
				commandFinished()
			}()
		},
	}
	return handler, recorder
}

func immediateNoopCommandHandler(_ context.Context, _ lcontext.LContext, _ int, _ []string, commandFinished func()) {
	commandFinished()
}

func (r *sessionDispatchRecorder) waitForStart(t *testing.T) recordedCommand {
	t.Helper()

	select {
	case started := <-r.starts:
		return started
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for dispatched session command")
		return recordedCommand{}
	}
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
	handler := newSessionTestHandler("session-generation-user")
	readServerMessage(t, handler.serverMessages)

	startPayload := mustSessionPayload(t, session.Spec{Mode: omode.TailClient, Regex: "ERROR"})
	updatePayload := mustSessionPayload(t, session.Spec{Mode: omode.TailClient, Regex: "WARN"})

	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3, []string{"SESSION", "START", startPayload}, func() {})
	if message := readServerMessage(t, handler.serverMessages); message != sessionAckStartOKPrefix+" 1" {
		t.Fatalf("unexpected session start ack: %q", message)
	}

	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3, []string{"SESSION", "UPDATE", updatePayload}, func() {})
	if message := readServerMessage(t, handler.serverMessages); message != sessionAckUpdateOKPrefix+" 2" {
		t.Fatalf("unexpected session update ack: %q", message)
	}
}

func waitForContextDone(t *testing.T, ctx context.Context) {
	t.Helper()

	select {
	case <-ctx.Done():
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for context cancellation")
	}
}
