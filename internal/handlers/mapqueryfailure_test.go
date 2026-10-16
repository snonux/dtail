package handlers

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/session"
)

// duplicateSelectQuery is a query older clients send but dserver rejects.
const duplicateSelectQuery = "from STATS select count($time),count($time) group by $time"

// TestFailedMapCommandRejectsLaterReads sends the legacy command stream of
// an older client whose query dserver rejects: the read commands following
// the failed map command must not stream the file as plain payload, each
// reports the failure, and the session ends with its close handshake.
func TestFailedMapCommandRejectsLaterReads(t *testing.T) {
	for _, mode := range []omode.Mode{omode.MapClient, omode.TailClient} {
		t.Run(mode.String(), func(t *testing.T) {
			output := runLegacyCommandStream(t, session.Spec{
				Mode:  mode,
				Files: []string{writeTestStatsFile(t, 25)},
				Query: duplicateSelectQuery,
				Regex: ".",
			})
			if strings.Contains(output, "MAPREDUCE:STATS") {
				t.Fatalf("read of a session with a failed map command streamed payload: %q", output)
			}
			for _, want := range []string{
				"'select' clause has 'count($time)' more than once",
				protocol.HiddenCommandFailedPrefix + protocol.CommandFailureMapQuery,
				".syn close connection",
			} {
				if !strings.Contains(output, want) {
					t.Fatalf("output lacks %q: %q", want, output)
				}
			}
		})
	}
}

// runLegacyCommandStream writes spec's commands in one input batch, as a
// client does, waits until the session ended and returns its output.
func runLegacyCommandStream(t *testing.T, spec session.Spec) string {
	t.Helper()
	commands, err := spec.Commands()
	if err != nil {
		t.Fatalf("build commands: %v", err)
	}
	handler := newMapTestHandler(t)
	commandWg := wrapHandlerCommandsForJoin(handler)
	output := &testOutput{}
	var writeMu sync.Mutex
	readerDone := startTestReader(handler, output, &writeMu)

	var frames strings.Builder
	frames.WriteString(encodeTestCommand(protocol.InputBatchBeginCommand))
	for _, command := range commands {
		frames.WriteString(encodeTestCommand(command))
	}
	frames.WriteString(encodeTestCommand(protocol.InputBatchCompleteCommand))
	writeMu.Lock()
	_, err = handler.Write([]byte(frames.String()))
	writeMu.Unlock()
	if err != nil {
		t.Fatalf("write commands: %v", err)
	}

	select {
	case <-handler.Done():
	case <-time.After(20 * time.Second):
		t.Fatalf("session did not end after its map command failed: %q", output.String())
	}
	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not observe EOF after handler shutdown")
	}
	waitForCommandJoin(t, commandWg, 10*time.Second)
	return output.String()
}

// TestMapQueryFailuresAreScopedToTheirGeneration checks that a failed map
// command of one command stream (legacy generation 0 or a session
// generation) does not reject the reads of another.
func TestMapQueryFailuresAreScopedToTheirGeneration(t *testing.T) {
	var failures mapQueryFailures
	if failures.failed(0) || failures.failed(1) {
		t.Fatal("no map command failed yet")
	}
	failures.mark(0)
	if !failures.failed(0) || failures.failed(1) {
		t.Fatal("a failure of the legacy stream must reject only its reads")
	}
	failures.mark(2)
	if failures.failed(0) || failures.failed(1) || !failures.failed(2) {
		t.Fatal("a failure of generation 2 must reject only its reads")
	}
	failures.clear(1)
	if !failures.failed(2) {
		t.Fatal("a successful map command of generation 1 must keep the failure of generation 2")
	}
	failures.clear(2)
	if failures.failed(2) {
		t.Fatal("a successful map command of generation 2 must clear its failure")
	}
}

// TestSessionStartRejectsDuplicateSelect checks the interactive bootstrap:
// a SESSION START whose query selects a field twice is rejected with the
// reason, and none of its commands are dispatched.
func TestSessionStartRejectsDuplicateSelect(t *testing.T) {
	handler, recorder := newQuerySessionDispatchTestHandler("session-duplicate-select-user")
	readServerMessage(t, handler.serverMessages)

	payload := mustSessionPayload(t, session.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Query: duplicateSelectQuery,
		Regex: ".",
	})
	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3,
		[]string{"SESSION", "START", payload}, func() {})

	assertDuplicateSelectRejected(t, handler)
	if handler.sessionState.activeSession() {
		t.Fatal("a rejected SESSION START must not start the session")
	}
	assertNoDispatchedCommand(t, recorder)
}

// TestSessionUpdateRejectsDuplicateSelect checks :reload: an update whose
// query selects a field twice is rejected with the reason, while the running
// generation keeps running untouched.
func TestSessionUpdateRejectsDuplicateSelect(t *testing.T) {
	handler, recorder := newQuerySessionDispatchTestHandler("session-reload-duplicate-select-user")
	readServerMessage(t, handler.serverMessages)

	spec := session.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Query: "from STATS select count($time) group by $time",
		Regex: ".",
	}
	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3,
		[]string{"SESSION", "START", mustSessionPayload(t, spec)}, func() {})
	if message := readServerMessage(t, handler.serverMessages); message != sessionAckStartOKPrefix+" 1" {
		t.Fatalf("unexpected session start ack: %q", message)
	}
	started := []recordedCommand{recorder.waitForStart(t), recorder.waitForStart(t)}

	spec.Query = duplicateSelectQuery
	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 4,
		[]string{"SESSION", "UPDATE", "2", mustSessionPayload(t, spec)}, func() {})

	assertDuplicateSelectRejected(t, handler)
	if generation := handler.sessionState.currentGeneration(); generation != 1 {
		t.Fatalf("generation = %d after a rejected update, want 1", generation)
	}
	for _, command := range started {
		if command.ctx.Err() != nil {
			t.Fatalf("a rejected update cancelled the running command %q", command.command)
		}
	}
	assertNoDispatchedCommand(t, recorder)
}

// TestSessionRejectsAFailedMapCommand covers a map command of a session
// that fails although its query passed validation (e.g. when creating its
// aggregate): the request is rejected and its reads are not dispatched.
func TestSessionRejectsAFailedMapCommand(t *testing.T) {
	handler, recorder := newQuerySessionDispatchTestHandler("session-failed-map-user")
	readServerMessage(t, handler.serverMessages)
	handler.commands["map"] = func(ctx context.Context, _ lcontext.LContext, _ int, _ []string, commandFinished func()) {
		handler.mapQueryFailures.mark(sessionGenerationFromContext(ctx))
		commandFinished()
	}

	payload := mustSessionPayload(t, session.Spec{
		Mode:  omode.TailClient,
		Files: []string{"/var/log/app.log"},
		Query: "from STATS select count($time) group by $time",
		Regex: ".",
	})
	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3,
		[]string{"SESSION", "START", payload}, func() {})

	if message := readServerMessage(t, handler.serverMessages); message != sessionAckErrorPrefix+"invalid session query" {
		t.Fatalf("unexpected session start ack: %q", message)
	}
	if handler.sessionState.activeSession() {
		t.Fatal("a session whose map command failed must not stay started")
	}
	assertNoDispatchedCommand(t, recorder)
}

func assertDuplicateSelectRejected(t *testing.T, handler *ServerHandler) {
	t.Helper()
	message := readServerMessage(t, handler.serverMessages)
	if !strings.HasPrefix(message, sessionAckErrorPrefix+"invalid session spec: ") ||
		!strings.Contains(message, "'select' clause has 'count($time)' more than once") {
		t.Fatalf("unexpected session error: %q", message)
	}
}

func assertNoDispatchedCommand(t *testing.T, recorder *sessionDispatchRecorder) {
	t.Helper()
	select {
	case command := <-recorder.starts:
		t.Fatalf("unexpected dispatched command %q", command.command)
	default:
	}
}
