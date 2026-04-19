package handlers

import (
	"bytes"
	"context"

	"github.com/mimecast/dtail/internal/io/line"
)

// ChannellessLineProcessor adapts the channel-less processor interface to work
// with the existing channel-based handler infrastructure. It holds a context so
// that ProcessLine can abort the blocking send when the consumer (handler's Read
// loop) has stopped due to a client disconnect or context cancellation, instead
// of leaking the goroutine, file descriptor, buffer, limiter slot, and
// wait-group entry indefinitely.
type ChannellessLineProcessor struct {
	ctx       context.Context
	lines     chan<- *line.Line
	globID    string
	lineCount uint64
}

var _ line.Processor = (*ChannellessLineProcessor)(nil)

// NewChannellessLineProcessor creates a processor that forwards lines to the
// provided channel. The ctx is used to abort ProcessLine when the consumer has
// gone away; callers should pass the same context that governs the read loop.
func NewChannellessLineProcessor(ctx context.Context, lines chan<- *line.Line, globID string) *ChannellessLineProcessor {
	return &ChannellessLineProcessor{
		ctx:    ctx,
		lines:  lines,
		globID: globID,
	}
}

// ProcessLine sends a line through the channel. It returns ctx.Err() if the
// context is cancelled before the send completes, allowing the calling read
// goroutine to stop producing lines and release its resources promptly.
func (p *ChannellessLineProcessor) ProcessLine(lineContent *bytes.Buffer, lineNum uint64, sourceID string) error {
	p.lineCount++

	// Create a line object that matches what the original implementation expects.
	l := line.New(lineContent, lineNum, 100, sourceID)

	// Use a select with ctx.Done() so we do not block forever if the consumer
	// (handler's Read loop) has exited because of a client disconnect or
	// context cancellation.
	select {
	case p.lines <- l:
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

// Flush does nothing for this implementation
func (p *ChannellessLineProcessor) Flush() error {
	return nil
}

// Close does nothing for this implementation
func (p *ChannellessLineProcessor) Close() error {
	return nil
}