package fs

import (
	"context"
	"fmt"

	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/regex"
)

// GrepProcessor handles grep-style filtering
type GrepProcessor struct {
	regex    regex.Regex
	plain    bool
	hostname string

	// Context handling
	beforeContext int
	afterContext  int
	maxCount      int

	// State for context processing
	matchCount     int
	afterRemaining int
	beforeBuffer   [][]byte
	beforeLineNums []int
}

// NewGrepProcessor creates a new grep processor
func NewGrepProcessor(re regex.Regex, plain, noColor bool, hostname string, beforeContext, afterContext, maxCount int) *GrepProcessor {
	gp := &GrepProcessor{
		regex:          re,
		plain:          plain,
		hostname:       hostname,
		beforeContext:  beforeContext,
		afterContext:   afterContext,
		maxCount:       maxCount,
		matchCount:     0,
		afterRemaining: 0,
	}

	if beforeContext > 0 {
		gp.beforeBuffer = make([][]byte, 0, beforeContext)
		gp.beforeLineNums = make([]int, 0, beforeContext)
	}

	return gp
}

func (gp *GrepProcessor) Initialize(ctx context.Context) error {
	return nil
}

func (gp *GrepProcessor) Cleanup() error {
	return nil
}

// ProcessLine processes a single line for grep filtering with context support.
// Returns formatted output for matching lines and their context, or nil for non-matching lines.
// Handles before/after context lines and respects maxCount limit.
func (gp *GrepProcessor) ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) ([]byte, bool) {
	isMatch := gp.regex.Match(line)

	// Handle lines that don't match the regex
	if !isMatch {
		// Handle after context lines (only for non-matching lines)
		if gp.afterRemaining > 0 {
			gp.afterRemaining--
			// Send this line as context
			return gp.formatLine(line, lineNum, filePath, stats, sourceID), true
		}
		// If we have before context, buffer this line
		if gp.beforeContext > 0 {
			// Make a copy of the line for buffering
			lineCopy := make([]byte, len(line))
			copy(lineCopy, line)

			// Add to buffer, removing oldest if at capacity
			if len(gp.beforeBuffer) >= gp.beforeContext {
				gp.beforeBuffer = gp.beforeBuffer[1:]
				gp.beforeLineNums = gp.beforeLineNums[1:]
			}
			gp.beforeBuffer = append(gp.beforeBuffer, lineCopy)
			gp.beforeLineNums = append(gp.beforeLineNums, lineNum)
		}
		return nil, false
	}

	// Line matches the regex
	gp.matchCount++

	// Check if we've reached maxCount
	if gp.maxCount > 0 && gp.matchCount > gp.maxCount {
		return nil, false
	}

	// Stats will be updated by DirectProcessor when the line is actually sent

	// Build result with before context, current line, and set up after context
	var result []byte

	// First, output any before context lines
	if gp.beforeContext > 0 {
		for i, beforeLine := range gp.beforeBuffer {
			beforeLineNum := gp.beforeLineNums[i]
			formatted := gp.formatLine(beforeLine, beforeLineNum, filePath, stats, sourceID)
			result = append(result, formatted...)
		}
		// Clear the buffer since we've used it
		gp.beforeBuffer = gp.beforeBuffer[:0]
		gp.beforeLineNums = gp.beforeLineNums[:0]
	}

	// Add the matching line
	formatted := gp.formatLine(line, lineNum, filePath, stats, sourceID)
	result = append(result, formatted...)

	// Set up after context (only if we're not already in after context mode)
	if gp.afterContext > 0 && gp.afterRemaining == 0 {
		gp.afterRemaining = gp.afterContext
	}

	return result, true
}

func (gp *GrepProcessor) Flush() []byte {
	return nil
}

// formatLine formats a line for output (shared by matching lines and context lines)
func (gp *GrepProcessor) formatLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) []byte {
	// Format output to match existing behavior
	if gp.plain {
		// If line already ends with a line ending, preserve it as-is
		// Otherwise, add LF for consistency with bufio.Scanner behavior
		if len(line) > 0 && (line[len(line)-1] == '\n' || (len(line) > 1 && line[len(line)-2] == '\r' && line[len(line)-1] == '\n')) {
			// Line already has line ending, preserve it exactly
			result := make([]byte, len(line))
			copy(result, line)
			return result
		} else {
			// Line doesn't have line ending, add LF
			result := make([]byte, len(line)+1)
			copy(result, line)
			result[len(line)] = '\n'
			return result
		}
	}

	// Format exactly like original basehandler.go for non-plain mode
	// REMOTE|{hostname}|{TransmittedPerc}|{Count}|{SourceID}|{Content}¬
	var transmittedPerc int
	var count uint64
	if stats != nil {
		transmittedPerc = stats.transmittedPerc()
		count = stats.totalLineCount()
	}

	// Build the protocol line
	protocolLine := fmt.Sprintf("REMOTE%s%s%s%3d%s%v%s%s%s%s",
		protocol.FieldDelimiter, gp.hostname, protocol.FieldDelimiter,
		transmittedPerc, protocol.FieldDelimiter, count, protocol.FieldDelimiter,
		sourceID, protocol.FieldDelimiter, string(line))

	// Server should never send colored output - client handles all colorization
	result := make([]byte, len(protocolLine)+1)
	copy(result, protocolLine)
	result[len(protocolLine)] = '\n'

	return result
}
