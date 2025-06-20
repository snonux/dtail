package fs

import (
	"context"
	"fmt"
	"github.com/mimecast/dtail/internal/protocol"
)

// CatProcessor handles cat-style output
type CatProcessor struct {
	plain      bool
	hostname   string
	serverless bool
}

// NewCatProcessor creates a new cat processor
func NewCatProcessor(plain, noColor bool, hostname string, serverless bool) *CatProcessor {
	// Debug: log the parameters
	// fmt.Fprintf(os.Stderr, "DEBUG CatProcessor: hostname='%s', serverless=%v, plain=%v\n", hostname, serverless, plain)
	
	return &CatProcessor{
		plain:      plain,
		hostname:   hostname,
		serverless: serverless,
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
// In non-plain mode in server context, it returns just the content - the baseHandler will format the protocol.
// In non-plain mode in serverless context, it formats the output with REMOTE protocol.
// Returns the line content and true (cat always outputs all lines).
func (cp *CatProcessor) ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) ([]byte, bool) {
	// Update stats for matched line (cat always matches all lines)
	if stats != nil {
		stats.updateLineMatched()
	}

	// In plain mode, just return the line content
	if cp.plain {
		result := make([]byte, len(line))
		copy(result, line)
		return result, true
	}

	// In non-plain serverless mode, we need to format with REMOTE protocol
	// since there's no server baseHandler to do it for us
	if cp.serverless {
		// Format exactly like original basehandler.go for non-plain mode
		// REMOTE|{hostname}|{TransmittedPerc}|{Count}|{SourceID}|{Content}
		var transmittedPerc int
		var count uint64
		if stats != nil {
			// For cat, we always transmit all matched lines, so transmittedPerc should be 100
			transmittedPerc = 100
			count = stats.totalLineCount()
		}

		// Use actual hostname from system, not "serverless"
		actualHostname := getHostname()

		// Build the protocol line without the message delimiter
		protocolLine := fmt.Sprintf("REMOTE%s%s%s%3d%s%v%s%s%s%s",
			protocol.FieldDelimiter, actualHostname, protocol.FieldDelimiter,
			transmittedPerc, protocol.FieldDelimiter, count, protocol.FieldDelimiter,
			sourceID, protocol.FieldDelimiter, string(line))

		// Return formatted line without color reset prefix
		// The ColorWriter will handle proper coloring
		result := []byte(protocolLine)
		return result, true
	}

	// In server mode, just return the line content
	// The baseHandler will handle protocol formatting
	result := make([]byte, len(line))
	copy(result, line)
	return result, true
}

func (cp *CatProcessor) Flush() []byte {
	// No need to add color reset codes here
	// The ColorWriter handles all coloring
	return nil
}

