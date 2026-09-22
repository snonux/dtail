package handlers

import (
	"io"
	"testing"

	"github.com/mimecast/dtail/internal/clients/clientlog"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
	maprclient "github.com/mimecast/dtail/internal/mapr/client"
	"github.com/mimecast/dtail/internal/protocol"
)

// TestMaprHandlerOutcome feeds a mapreduce client handler the hidden messages
// a session ends with and checks how it reports the session's end: complete
// only after the server's close handshake, and with the first failed command
// the server reported.
func TestMaprHandlerOutcome(t *testing.T) {
	const (
		capabilities = protocol.HiddenCapabilitiesPrefix + protocol.CapabilityQueryUpdateV1 + " " +
			protocol.CapabilityCommandFailureV1
		closeSync = ".syn close connection"
		noFile    = protocol.HiddenCommandFailedPrefix + "read: no file to read\n"
		noPerm    = protocol.HiddenCommandFailedPrefix + "read: no permission to read file\n"
	)
	tests := []struct {
		name     string
		messages []string
		want     SessionOutcome
	}{
		{name: "closed by the server", messages: []string{capabilities, closeSync},
			want: SessionOutcome{Completed: true}},
		{name: "cut short", messages: []string{capabilities}},
		{name: "cut short by an unknown hidden message", messages: []string{capabilities, ".syn close later"}},
		{name: "failed command then closed", messages: []string{capabilities, noFile, closeSync},
			want: SessionOutcome{Completed: true, Failure: "read: no file to read"}},
		{name: "first failed command is kept", messages: []string{capabilities, noFile, noPerm, closeSync},
			want: SessionOutcome{Completed: true, Failure: "read: no file to read"}},
		{name: "failed command and cut short", messages: []string{capabilities, noPerm},
			want: SessionOutcome{Failure: "read: no permission to read file"}},
		{name: "failed command without reason", messages: []string{capabilities,
			protocol.HiddenCommandFailedPrefix, closeSync},
			want: SessionOutcome{Completed: true, Failure: "unknown reason"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := mapr.NewQuery("from STATS select count($line)", logging.NopLogger{})
			if err != nil {
				t.Fatalf("NewQuery() error = %v", err)
			}
			handler := NewMaprHandler("srv1", maprclient.NewSessionState(query, logging.NopLogger{}),
				clientlog.NopLogger{})
			// Take the close acknowledgement the handler sends.
			go func() { _, _ = io.Copy(io.Discard, handler) }()
			defer handler.Shutdown()

			for _, message := range tt.messages {
				if _, err := handler.Write(append([]byte(message), protocol.MessageDelimiter)); err != nil {
					t.Fatalf("Write(%q) error = %v", message, err)
				}
			}
			if got := handler.Outcome(); got != tt.want {
				t.Fatalf("Outcome() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
