package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/session"
)

// TestSessionStartWithoutFilesHoldsTheSession pins down what the query
// builder of dmap relies on (clients.builderHoldSpec), with no change to
// dserver: a SESSION START in tail mode without files is acknowledged,
// dispatches no command and leaves the session active (keepAlive), so the
// connection stays for the client's probes until the client ends it. The
// map mode can not hold a session that way: without a query its spec is
// rejected, which is why the builder holds its sessions in tail mode.
func TestSessionStartWithoutFilesHoldsTheSession(t *testing.T) {
	handler, recorder := newSessionDispatchTestHandler("session-hold-user")
	readServerMessage(t, handler.serverMessages)

	hold := session.Spec{Mode: omode.TailClient, Options: "quiet=true", Regex: "."}
	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3,
		[]string{"SESSION", "START", mustSessionPayload(t, hold)}, func() {})
	if message := readSessionAck(t, handler); message != sessionAckStartOKPrefix+" 1" {
		t.Fatalf("session start acknowledgement = %q", message)
	}
	if !handler.sessionState.activeSession() || !handler.sessionState.keepAlive() {
		t.Fatal("the session without files is not kept")
	}
	select {
	case started := <-recorder.starts:
		t.Fatalf("a session without files dispatched %q", started.command)
	default:
	}
}

func TestSessionStartInMapModeNeedsAQuery(t *testing.T) {
	handler := newSessionTestHandler("session-map-hold-user")
	readServerMessage(t, handler.serverMessages)

	spec := session.Spec{Mode: omode.MapClient, Files: []string{"/var/log/app.log"}}
	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3,
		[]string{"SESSION", "START", mustSessionPayload(t, spec)}, func() {})
	message := readSessionAck(t, handler)
	if !strings.HasPrefix(message, sessionAckErrorPrefix) || !strings.Contains(message, "missing session query") {
		t.Fatalf("acknowledgement of a map session without query = %q, want its rejection", message)
	}
	if handler.sessionState.activeSession() {
		t.Fatal("a rejected session start left the session active")
	}
}
