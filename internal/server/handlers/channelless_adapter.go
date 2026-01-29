package handlers

import (
	"bytes"

	"github.com/mimecast/dtail/internal/io/line"
)

// ChannellessLineProcessor adapts the channel-less processor to work with the existing handler infrastructure
type ChannellessLineProcessor struct {
	lines      chan<- *line.Line
	globID     string
	lineCount  uint64
}

var _ line.Processor = (*ChannellessLineProcessor)(nil)

// NewChannellessLineProcessor creates a processor that sends lines to the existing channel
func NewChannellessLineProcessor(lines chan<- *line.Line, globID string) *ChannellessLineProcessor {
	return &ChannellessLineProcessor{
		lines:  lines,
		globID: globID,
	}
}

// ProcessLine sends a line through the channel
func (p *ChannellessLineProcessor) ProcessLine(lineContent *bytes.Buffer, lineNum uint64, sourceID string) error {
	p.lineCount++
	
	// Create a line object that matches what the original implementation expects
	l := line.New(lineContent, lineNum, 100, sourceID)
	
	// Send through the channel (blocking to ensure no lines are lost)
	p.lines <- l
	return nil
}

// Flush does nothing for this implementation
func (p *ChannellessLineProcessor) Flush() error {
	return nil
}

// Close does nothing for this implementation
func (p *ChannellessLineProcessor) Close() error {
	return nil
}