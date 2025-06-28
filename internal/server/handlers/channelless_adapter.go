package handlers

import (
	"bytes"
	"fmt"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/io/pool"
)

// ChannellessLineProcessor adapts the channel-less processor to work with the existing handler infrastructure
type ChannellessLineProcessor struct {
	lines      chan<- *line.Line
	globID     string
	lineCount  uint64
}

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
	
	// Send through the channel
	select {
	case p.lines <- l:
		return nil
	default:
		// Channel full, recycle the buffer
		pool.RecycleBytesBuffer(lineContent)
		return fmt.Errorf("lines channel full")
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