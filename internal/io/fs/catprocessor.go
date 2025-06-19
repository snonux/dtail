package fs

import (
	"context"
	"fmt"

	"github.com/mimecast/dtail/internal/protocol"
)

// CatProcessor handles cat-style output
type CatProcessor struct {
	plain    bool
	hostname string
}

// NewCatProcessor creates a new cat processor
func NewCatProcessor(plain, noColor bool, hostname string) *CatProcessor {
	return &CatProcessor{
		plain:    plain,
		hostname: hostname,
	}
}

func (cp *CatProcessor) Initialize(ctx context.Context) error {
	return nil
}

func (cp *CatProcessor) Cleanup() error {
	return nil
}

// ProcessLine processes a single line for cat output.
// In plain mode, it preserves the original line exactly including line endings.
// In non-plain mode, it formats the line according to DTail protocol with optional colorization.
// Returns the formatted line and true (cat always outputs all lines).
func (cp *CatProcessor) ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) ([]byte, bool) {
	// Update stats for matched line (cat always matches all lines)
	if stats != nil {
		stats.updateLineMatched()
	}

	// Format output to match existing behavior
	if cp.plain {
		// In plain mode, preserve the original line exactly as it is
		// The line already includes its original line ending
		result := make([]byte, len(line))
		copy(result, line)
		return result, true
	}

	// Format exactly like original basehandler.go for non-plain mode
	// REMOTE|{hostname}|{TransmittedPerc}|{Count}|{SourceID}|{Content}¬
	var transmittedPerc int
	var count uint64
	if stats != nil {
		// For cat, we always transmit all matched lines, so transmittedPerc should be 100
		transmittedPerc = 100
		count = stats.totalLineCount()
	}

	// Build the protocol line
	protocolLine := fmt.Sprintf("REMOTE%s%s%s%3d%s%v%s%s%s%s",
		protocol.FieldDelimiter, cp.hostname, protocol.FieldDelimiter,
		transmittedPerc, protocol.FieldDelimiter, count, protocol.FieldDelimiter,
		sourceID, protocol.FieldDelimiter, string(line))

	// Server should never send colored output - client handles all colorization
	result := make([]byte, len(protocolLine)+1)
	copy(result, protocolLine)
	result[len(protocolLine)] = '\n'

	return result, true
}

func (cp *CatProcessor) Flush() []byte {
	// Server should not send color codes - client handles colorization
	return nil
}
