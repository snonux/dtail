package handlers

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/line"
)

// TestProcessLineReturnsWhenContextCancelled is a negative test that verifies
// ProcessLine does not block forever when the consumer (channel reader) has stopped.
// Before the fix, p.lines <- l would block indefinitely if the consumer exited,
// leaking the file descriptor, buffer, limiter slot and wait-group entry.
func TestProcessLineReturnsWhenContextCancelled(t *testing.T) {
	t.Parallel()

	// Create an unbuffered channel — no reader will ever consume from it,
	// simulating a client disconnect or cancelled context.
	lines := make(chan *line.Line)

	ctx, cancel := context.WithCancel(context.Background())

	proc := NewChannellessLineProcessor(ctx, lines, "test-glob")

	// Cancel the context immediately to simulate consumer gone.
	cancel()

	done := make(chan error, 1)
	go func() {
		content := bytes.NewBufferString("some log line")
		done <- proc.ProcessLine(content, 1, "test-source")
	}()

	select {
	case err := <-done:
		// ProcessLine must return the context error, not nil, so the caller
		// knows to stop producing lines.
		if err == nil {
			t.Fatal("expected a non-nil error (context.Canceled) but got nil")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ProcessLine blocked forever after context cancellation — bug reproduced")
	}
}

// TestProcessLineSucceedsWhenConsumerReads verifies the happy path: when the
// consumer is active, ProcessLine successfully sends the line and returns nil.
func TestProcessLineSucceedsWhenConsumerReads(t *testing.T) {
	t.Parallel()

	// Buffered channel so the send can complete without a separate goroutine reader.
	lines := make(chan *line.Line, 1)

	ctx := context.Background()
	proc := NewChannellessLineProcessor(ctx, lines, "test-glob")

	content := bytes.NewBufferString("hello world")
	if err := proc.ProcessLine(content, 1, "src"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	select {
	case <-lines:
		// line received correctly
	default:
		t.Fatal("expected a line in the channel but none was found")
	}
}
