package fs

import (
	"bytes"
	"context"
	"time"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

// AggregateLineProcessor feeds lines to an existing aggregate via channels
type AggregateLineProcessor struct {
	linesCh    chan<- *line.Line
	re         regex.Regex
	hostname   string
	ltx        lcontext.LContext
	lineNum    int
	isTailing  bool // Whether this is for a tail operation that should keep running
}

// NewAggregateLineProcessor creates a processor that feeds lines to an aggregate
func NewAggregateLineProcessor(linesCh chan<- *line.Line, re regex.Regex, hostname string, ltx lcontext.LContext) *AggregateLineProcessor {
	return &AggregateLineProcessor{
		linesCh:   linesCh,
		re:        re,
		hostname:  hostname,
		ltx:       ltx,
		lineNum:   0,
		isTailing: false,
	}
}

// NewAggregateLineProcessorForTail creates a processor for tail operations that feeds lines to an aggregate
func NewAggregateLineProcessorForTail(linesCh chan<- *line.Line, re regex.Regex, hostname string, ltx lcontext.LContext) *AggregateLineProcessor {
	return &AggregateLineProcessor{
		linesCh:   linesCh,
		re:        re,
		hostname:  hostname,
		ltx:       ltx,
		lineNum:   0,
		isTailing: true,
	}
}

func (p *AggregateLineProcessor) ProcessLine(lineBuf []byte, lineNum int, filePath string, stats *stats, sourceID string) (result []byte, shouldSend bool) {
	p.lineNum++
	
	// For MapReduce operations, don't apply regex filtering here - let the aggregate handle it
	// The aggregate's log parser and WHERE clause will do the proper filtering
	
	// Create a line object similar to what the channel-based system creates
	// Make a copy of the line buffer to avoid issues with slice reuse
	lineCopy := make([]byte, len(lineBuf))
	copy(lineCopy, lineBuf)
	content := bytes.NewBuffer(lineCopy)
	l := line.New(content, uint64(p.lineNum), 100, sourceID)
	
	// Send the line to the aggregate via the channel (blocking send to avoid data loss)
	p.linesCh <- l
	
	// Don't send output directly since the aggregate will handle serialization
	return nil, false
}

func (p *AggregateLineProcessor) Flush() []byte {
	// For tail operations, don't close the channel as we want to keep following
	if !p.isTailing {
		// Close the lines channel to signal end of input
		// Add a small delay to ensure all lines are processed before closing
		time.Sleep(10 * time.Millisecond)
		close(p.linesCh)
	}
	return nil
}

func (p *AggregateLineProcessor) Initialize(ctx context.Context) error {
	return nil
}

func (p *AggregateLineProcessor) Cleanup() error {
	return nil
}