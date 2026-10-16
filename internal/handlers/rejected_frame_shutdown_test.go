package handlers

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/protocol"
)

// rejectedFrame is a command frame the dispatcher rejects before any handler
// runs (commandDispatcher.handleCommand), with the message that reports it.
type rejectedFrame struct {
	frame  string
	report string
}

// rejectedFrames are the command frames commandDispatcher.handleCommand
// rejects before any handler runs, each with what reports the rejection to
// the client: the failed-command message, or for a protocol version
// mismatch, which gets none, its error message.
func rejectedFrames() map[string]rejectedFrame {
	mismatch := "protocol 99.0 base64 " +
		base64.StdEncoding.EncodeToString([]byte("cat test.log .")) + ";"
	return map[string]rejectedFrame{
		"invalid timeout prefix": {encodeTestCommand("timeout x cat test.log ."),
			protocol.HiddenCommandFailedPrefix + commandFailureRejected},
		"unparsable options": {encodeTestCommand("cat:bogus test.log ."),
			protocol.HiddenCommandFailedPrefix + commandFailureRejected},
		"undecodable frame": {"protocol " + protocol.ProtocolCompat + " base64 !!!;",
			protocol.HiddenCommandFailedPrefix + commandFailureProtocol},
		"protocol version mismatch": {mismatch, "does not match client protocol version '99.0'"},
	}
}

// TestServerHandlerRejectedOnlyCommandEndsSession: a session whose only
// command frame is rejected before any handler ran admits no command, so no
// command finishes it; the dispatcher's rejection hook
// (ServerHandler.frameRejected) starts its idle shutdown instead. Before, such
// a session sent its failure report but no close handshake and stayed open
// until IdleSessionTimeoutS. The session must end well under a second
// (runFailingFramesSession), with the report before exactly one close
// handshake, for older, escaped-only and control-frame clients.
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
				stream := runFailingFramesSession(t, handler, encodeTestCommands(first...)+rejected.frame)
				if closes := strings.Count(stream, ".syn close connection"); closes != 1 {
					t.Fatalf("%d close handshakes, want 1: %q", closes, stream)
				}
				beforeClose, _, _ := strings.Cut(stream, ".syn close connection")
				if !strings.Contains(beforeClose, rejected.report) {
					t.Fatalf("rejection report %q missing before the close handshake: %q",
						rejected.report, stream)
				}
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
