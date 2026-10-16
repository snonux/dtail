package handlers

import (
	"context"
	"testing"

	"github.com/mimecast/dtail/internal/protocol"
)

// TestShutdownReportsIncompleteMapResult checks the session's graceful
// shutdown: a map result whose final serialization could not hand every
// group to the output is reported as a failed command before the close
// handshake (a scheduled job then writes no outfile, see
// protocol.CommandFailureMapIncomplete), while a complete one closes without
// a report.
func TestShutdownReportsIncompleteMapResult(t *testing.T) {
	const groups = 3
	incompleteReport := protocol.HiddenCommandFailedPrefix + protocol.CommandFailureMapIncomplete + "\n"
	tests := []struct {
		name string
		// output is the aggregate's result channel; an unbuffered channel
		// nobody reads keeps the final sends blocked.
		output     chan string
		cancelCtx  bool
		wantReport bool
	}{
		{
			name:   "complete result closes normally",
			output: make(chan string, groups),
		},
		{
			name:       "canceled final serialization is reported",
			output:     make(chan string),
			cancelCtx:  true,
			wantReport: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newReadTestHandler()
			// No client acknowledges the close here; do not wait for one.
			handler.acknowledgeClose()
			aggregate := newPopulatedTestAggregate(t, groups)
			aggregate.PrepareOutput(context.Background(), tt.output)
			handler.setAggregate(aggregate)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelCtx {
				cancel()
			}
			handler.shutdown(ctx)

			// The report is queued synchronously, before shutdown requests
			// the close handshake; this handler has no attached reader, so
			// the close message is sent asynchronously and may be dropped
			// once the handler is done. Only the report is checked.
			reported := false
			for len(handler.serverMessages) > 0 {
				if <-handler.serverMessages == incompleteReport {
					reported = true
				}
			}
			if reported != tt.wantReport {
				t.Fatalf("incomplete map result reported = %v, want %v", reported, tt.wantReport)
			}
			if !tt.wantReport && len(tt.output) != groups {
				t.Fatalf("final result sent %d groups, want %d", len(tt.output), groups)
			}
		})
	}
}
