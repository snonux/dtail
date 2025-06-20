package fs

import (
	"context"
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
// In non-plain mode, it returns just the content - the baseHandler will format the protocol.
// Returns the line content and true (cat always outputs all lines).
func (cp *CatProcessor) ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) ([]byte, bool) {
	// Update stats for matched line (cat always matches all lines)
	if stats != nil {
		stats.updateLineMatched()
	}

	// In both plain and non-plain modes, just return the line content
	// The baseHandler will handle protocol formatting for non-plain mode
	result := make([]byte, len(line))
	copy(result, line)
	return result, true
}

func (cp *CatProcessor) Flush() []byte {
	// Server should not send color codes - client handles colorization
	return nil
}
