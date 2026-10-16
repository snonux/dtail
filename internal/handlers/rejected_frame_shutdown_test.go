package handlers

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/session"
	userserver "github.com/mimecast/dtail/internal/sessionuser"
)

// rejectedFrame is a command frame the dispatcher rejects before any handler
// runs (commandDispatcher.handleCommand), with the message that reports it
// and the frames the client answers the close handshake with.
type rejectedFrame struct {
	frame  string
	report string
	ack    string
}

// mismatchReport is the error message of a frame of protocol version 99.0.
const mismatchReport = "does not match client protocol version '99.0'"

// encodeMismatchCommand frames command like a client of protocol version
// 99.0, which this server does not speak.
func encodeMismatchCommand(command string) string {
	return "protocol 99.0 base64 " + base64.StdEncoding.EncodeToString([]byte(command)) + ";"
}

// rejectedFrames are the command frames commandDispatcher.handleCommand
// rejects before any handler runs, each with what reports the rejection to
// the client: the failed-command message, or for a protocol version
// mismatch, which gets none, its error message. A client of the other
// protocol version acknowledges the close in its own version; here it also
// sends its command once more first, which must get no error message after
// the close handshake (commandDispatcher.rejectProtocolVersion).
func rejectedFrames() map[string]rejectedFrame {
	ack := encodeTestCommand(".ack close connection")
	mismatch := encodeMismatchCommand("cat test.log .")
	return map[string]rejectedFrame{
		"invalid timeout prefix": {encodeTestCommand("timeout x cat test.log ."),
			protocol.HiddenCommandFailedPrefix + commandFailureRejected, ack},
		"unparsable options": {encodeTestCommand("cat:bogus test.log ."),
			protocol.HiddenCommandFailedPrefix + commandFailureRejected, ack},
		"undecodable frame": {"protocol " + protocol.ProtocolCompat + " base64 !!!;",
			protocol.HiddenCommandFailedPrefix + commandFailureProtocol, ack},
		"protocol version mismatch": {mismatch, mismatchReport,
			mismatch + encodeMismatchCommand(".ack close connection")},
	}
}

// checkRejectedFrameSession fails unless stream, all bytes of a session
// whose only command frame was rejected, holds the rejection's report
// exactly once before exactly one close handshake, and nothing after it.
func checkRejectedFrameSession(t *testing.T, stream string, rejected rejectedFrame) {
	t.Helper()
	if closes := strings.Count(stream, ".syn close connection"); closes != 1 {
		t.Fatalf("%d close handshakes, want 1: %q", closes, stream)
	}
	beforeClose, afterClose, _ := strings.Cut(stream, ".syn close connection")
	if !strings.Contains(beforeClose, rejected.report) {
		t.Fatalf("rejection report %q missing before the close handshake: %q",
			rejected.report, stream)
	}
	if reports := strings.Count(stream, rejected.report); reports != 1 {
		t.Fatalf("%d rejection reports, want 1: %q", reports, stream)
	}
	// The close handshake's frame ends with the message delimiter.
	if strings.TrimSuffix(afterClose, string([]byte{protocol.MessageDelimiter})) != "" {
		t.Fatalf("output after the close handshake: %q", afterClose)
	}
}

// TestServerHandlerRejectedOnlyCommandEndsSession: a session whose only
// command frame is rejected before any handler ran admits no command, so no
// command finishes it; the dispatcher's rejection hook
// (ServerHandler.frameRejected) starts its idle shutdown instead. Before, such
// a session sent its failure report but no close handshake and stayed open
// until IdleSessionTimeoutS. The session must end well under a second
// (runFailingFramesSession), with the report before exactly one close
// handshake and nothing after it, for older, escaped-only and control-frame
// clients. A client of another protocol version acknowledges the close in
// its own version, which must end the session as quickly
// (commandDispatcher.rejectProtocolVersion); before, that acknowledgement
// was rejected like any frame of that version, so the session waited for
// the close acknowledgement timeout (5 s) and sent mismatch errors after
// its close handshake.
func TestServerHandlerRejectedOnlyCommandEndsSession(t *testing.T) {
	clients := map[string][]string{
		"control-frame client": {protocol.EscapedFramingCommand, protocol.ControlFramesCommand},
	}
	for client, first := range settleTestClients {
		clients[client] = first
	}
	for client, first := range clients {
		for name, rejected := range rejectedFrames() {
			t.Run(client+"/"+name, func(t *testing.T) {
				handler := newFramingTestServerHandler(t, ServerCapabilities("linux", false))
				stream := runFailingFramesSessionAcking(t, handler, protocol.HiddenCapabilitiesPrefix,
					encodeTestCommands(first...)+rejected.frame, rejected.ack)
				checkRejectedFrameSession(t, stream, rejected)
				select {
				case <-handler.ackCloseReceived:
				default:
					t.Fatalf("session ended without the close acknowledgement: %q", stream)
				}
			})
		}
	}
}

// TestServerHandlerRejectedCommandInBatchDoesNotEndSessionEarly: a frame
// rejected inside an open input batch must not start the idle shutdown
// (claimIdleShutdown sees the open batch), so the cat that follows it in the
// batch delivers all of its lines before exactly one close handshake.
func TestServerHandlerRejectedCommandInBatchDoesNotEndSessionEarly(t *testing.T) {
	cat := idleShutdownBatchCatCommand(t, writeIdleShutdownBatchFile(t))
	for name, rejected := range rejectedFrames() {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 5; i++ {
				stream := runBatchedFailingSession(t, rejected.frame, cat)
				if closes := strings.Count(stream, ".syn close connection"); closes != 1 {
					t.Fatalf("iteration %d: %d close handshakes, want 1", i, closes)
				}
				beforeClose, _, _ := strings.Cut(stream, ".syn close connection")
				if !strings.Contains(beforeClose, rejected.report) {
					t.Fatalf("iteration %d: rejection report %q missing before the close handshake",
						i, rejected.report)
				}
				checkAllCatLines(t, beforeClose)
			}
		})
	}
}

// TestHealthHandlerRejectedFrameEndsSession: a health session whose command
// frame is rejected before any handler ran ends at once
// (HealthHandler.frameRejected), with the rejection's report before exactly
// one close handshake and nothing after it, also for a client of another
// protocol version acknowledging the close in its own version. Before, no
// handler ran and the session stayed open until the client gave up.
func TestHealthHandlerRejectedFrameEndsSession(t *testing.T) {
	for name, rejected := range rejectedFrames() {
		t.Run(name, func(t *testing.T) {
			handler, err := NewHealthHandler(context.Background(), &userserver.User{Name: "health"},
				config.DefaultMaxCommandFrameSize, "testhost", handlerTestLogger)
			if err != nil {
				t.Fatalf("NewHealthHandler: %v", err)
			}
			stream := runFailingFramesSessionAcking(t, handler, "", rejected.frame, rejected.ack)
			checkRejectedFrameSession(t, stream, rejected)
			select {
			case <-handler.ackCloseReceived:
			default:
				t.Fatalf("session ended without the close acknowledgement: %q", stream)
			}
		})
	}
}

// TestServerHandlerRejectedFrameKeepsBusySessionOpen: a frame rejected while
// a SESSION START keeps the session alive, or while an unbatched tail still
// runs, must not close the session (claimIdleShutdown sees the kept-alive
// session or the active command): its report arrives, but no close
// handshake follows.
func TestServerHandlerRejectedFrameKeepsBusySessionOpen(t *testing.T) {
	path := writeIdleShutdownBatchFile(t)
	busy := map[string]func(t *testing.T, handler *ServerHandler, output *testOutput,
		writeMu *sync.Mutex){
		"SESSION START": func(t *testing.T, handler *ServerHandler, output *testOutput, writeMu *sync.Mutex) {
			start, err := session.Spec{Mode: omode.CatClient, Files: []string{path}, Regex: "."}.StartCommand()
			if err != nil {
				t.Fatalf("build SESSION START: %v", err)
			}
			writeTestCommandsWithin(t, handler, writeMu, 10*time.Second, start)
			output.waitForContains(t, sessionAckStartOKPrefix, 5*time.Second)
		},
		"running tail": func(t *testing.T, handler *ServerHandler, _ *testOutput, writeMu *sync.Mutex) {
			commands, err := session.Spec{Mode: omode.TailClient, Files: []string{path}, Regex: "."}.Commands()
			if err != nil || len(commands) != 1 {
				t.Fatalf("build tail command: %v (%d commands)", err, len(commands))
			}
			writeTestCommandsWithin(t, handler, writeMu, 10*time.Second, commands[0])
			waitForActiveCommand(t, handler)
		},
	}
	for busyName, makeBusy := range busy {
		for name, rejected := range rejectedFrames() {
			t.Run(busyName+"/"+name, func(t *testing.T) {
				handler := newFramingTestServerHandler(t, ServerCapabilities("linux", false))
				output := &testOutput{}
				var writeMu sync.Mutex
				readerDone := startTestReader(handler, output, &writeMu)
				output.waitForContains(t, protocol.HiddenCapabilitiesPrefix, 5*time.Second)

				makeBusy(t, handler, output, &writeMu)
				writeTestFramesWithin(t, handler, &writeMu, 10*time.Second, rejected.frame)
				output.waitForContains(t, rejected.report, 5*time.Second)
				// A wrongly started idle shutdown sends its close handshake
				// at once; give it time to show.
				time.Sleep(200 * time.Millisecond)
				if strings.Contains(output.String(), ".syn close connection") {
					t.Fatalf("rejected frame closed a busy session: %q", output.String())
				}
				handler.Shutdown()
				select {
				case <-readerDone:
				case <-time.After(5 * time.Second):
					t.Fatal("reader did not observe EOF after handler shutdown")
				}
			})
		}
	}
}

// waitForActiveCommand waits until handler runs a command.
func waitForActiveCommand(t *testing.T, handler *ServerHandler) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, active := handler.PendingAndActive(); active > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
