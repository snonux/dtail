package fs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/regex"
)

// LineProcessor interface for channelless line-by-line processing
type LineProcessor interface {
	ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) (result []byte, shouldSend bool)
	Flush() []byte // For any buffered output (e.g., MapReduce)
	Initialize(ctx context.Context) error
	Cleanup() error
}

// DirectProcessor processes files without channels for better performance
type DirectProcessor struct {
	processor LineProcessor
	output    io.Writer
	stats     *stats
	ltx       lcontext.LContext
	sourceID  string // The globID for this file
}

// NewDirectProcessor creates a new direct processor
func NewDirectProcessor(processor LineProcessor, output io.Writer, globID string, ltx lcontext.LContext) *DirectProcessor {
	return &DirectProcessor{
		processor: processor,
		output:    output,
		stats:     &stats{}, // Create a new stats instance
		ltx:       ltx,
		sourceID:  globID,
	}
}

// ProcessFile processes a file directly without channels
func (dp *DirectProcessor) ProcessFile(ctx context.Context, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Initialize processor
	if err := dp.processor.Initialize(ctx); err != nil {
		return err
	}
	defer dp.processor.Cleanup()

	return dp.ProcessReader(ctx, file, filePath)
}

// ProcessReader processes an io.Reader directly without channels
func (dp *DirectProcessor) ProcessReader(ctx context.Context, reader io.Reader, filePath string) error {
	// Check if we need to preserve line endings (for cat in plain mode)
	if catProcessor, ok := dp.processor.(*CatProcessor); ok && catProcessor.plain {
		return dp.processReaderPreservingLineEndings(ctx, reader, filePath)
	}
	
	scanner := bufio.NewScanner(reader)
	
	// Set buffer size respecting MaxLineLength configuration
	maxLineLength := config.Server.MaxLineLength
	initialBufSize := 64 * 1024
	if maxLineLength < initialBufSize {
		initialBufSize = maxLineLength
	}
	scanner.Buffer(make([]byte, initialBufSize), maxLineLength)
	
	lineNum := 0
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		
		lineNum++
		line := scanner.Bytes()
		
		// Update position stats
		if dp.stats != nil {
			dp.stats.updatePosition()
		}
		
		// Process line directly  
		if result, shouldSend := dp.processor.ProcessLine(line, lineNum, filePath, dp.stats, dp.sourceID); shouldSend {
			if _, err := dp.output.Write(result); err != nil {
				return err
			}
			
			// Update transmission stats
			if dp.stats != nil {
				dp.stats.updateLineTransmitted()
			}
		}
	}
	
	// Flush any buffered output
	if final := dp.processor.Flush(); len(final) > 0 {
		if _, err := dp.output.Write(final); err != nil {
			return err
		}
	}
	
	return scanner.Err()
}

// processReaderPreservingLineEndings processes a reader while preserving original line endings
// and implementing line splitting for very long lines
func (dp *DirectProcessor) processReaderPreservingLineEndings(ctx context.Context, reader io.Reader, filePath string) error {
	buf := make([]byte, 8192)
	var remaining []byte
	lineNum := 0
	maxLineLength := config.Server.MaxLineLength
	warnedAboutLongLine := false
	
	
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		
		n, err := reader.Read(buf)
		if n > 0 {
			data := append(remaining, buf[:n]...)
			remaining = remaining[:0]
			
			// Process complete lines
			for {
				// Find next line ending (LF or CRLF)
				lfIndex := bytes.IndexByte(data, '\n')
				if lfIndex == -1 {
					// No complete line found
					// Check if the accumulated data exceeds max line length
					if len(data) >= maxLineLength {
						if !warnedAboutLongLine {
							// Note: we don't have server messages channel in channelless mode
							// so we'll just split without warning
							warnedAboutLongLine = true
						}
						// Split at max line length, add LF
						lineNum++
						splitLine := make([]byte, maxLineLength+1)
						copy(splitLine, data[:maxLineLength])
						splitLine[maxLineLength] = '\n'
						
						// Update position stats
						if dp.stats != nil {
							dp.stats.updatePosition()
						}
						
						// Process the split line
						if result, shouldSend := dp.processor.ProcessLine(splitLine, lineNum, filePath, dp.stats, dp.sourceID); shouldSend {
							if _, err := dp.output.Write(result); err != nil {
								return err
							}
							
							// Update transmission stats
							if dp.stats != nil {
								dp.stats.updateLineTransmitted()
							}
						}
						
						// Continue with remaining data
						data = data[maxLineLength:]
						continue
					} else {
						// Save for next iteration
						remaining = append(remaining, data...)
						break
					}
				}
				
				line := data[:lfIndex+1] // Include the LF
				data = data[lfIndex+1:]   // Continue with remaining data
				
				// Reset warning flag for new line
				warnedAboutLongLine = false
				
				// Check if this line exceeds max length and needs to be split
				if len(line) > maxLineLength {
					// Split the long line into chunks
					lineContent := line[:len(line)-1] // Remove the LF
					lineEnding := line[len(line)-1:]  // Keep the LF
					
					for len(lineContent) > 0 {
						lineNum++
						var chunk []byte
						if len(lineContent) > maxLineLength {
							chunk = make([]byte, maxLineLength+1)
							copy(chunk, lineContent[:maxLineLength])
							chunk[maxLineLength] = '\n'
							lineContent = lineContent[maxLineLength:]
						} else {
							chunk = make([]byte, len(lineContent)+len(lineEnding))
							copy(chunk, lineContent)
							copy(chunk[len(lineContent):], lineEnding)
							lineContent = nil
						}
						
						// Update position stats
						if dp.stats != nil {
							dp.stats.updatePosition()
						}
						
						// Process the chunk
						if result, shouldSend := dp.processor.ProcessLine(chunk, lineNum, filePath, dp.stats, dp.sourceID); shouldSend {
							if _, err := dp.output.Write(result); err != nil {
								return err
							}
							
							// Update transmission stats
							if dp.stats != nil {
								dp.stats.updateLineTransmitted()
							}
						}
					}
				} else {
					// Normal line processing
					lineNum++
					
					// Update position stats
					if dp.stats != nil {
						dp.stats.updatePosition()
					}
					
					// Process line directly (line includes original line ending)
					if result, shouldSend := dp.processor.ProcessLine(line, lineNum, filePath, dp.stats, dp.sourceID); shouldSend {
						if _, err := dp.output.Write(result); err != nil {
							return err
						}
						
						// Update transmission stats
						if dp.stats != nil {
							dp.stats.updateLineTransmitted()
						}
					}
				}
			}
		}
		
		if err == io.EOF {
			// Process any remaining data as the last line, respecting line length limit
			for len(remaining) > 0 {
				lineNum++
				
				var lineToProcess []byte
				if len(remaining) > maxLineLength {
					// Split the remaining data
					lineToProcess = make([]byte, maxLineLength+1)
					copy(lineToProcess, remaining[:maxLineLength])
					lineToProcess[maxLineLength] = '\n'
					remaining = remaining[maxLineLength:]
				} else {
					// Process all remaining data
					lineToProcess = remaining
					remaining = nil
				}
				
				// Update position stats
				if dp.stats != nil {
					dp.stats.updatePosition()
				}
				
				if result, shouldSend := dp.processor.ProcessLine(lineToProcess, lineNum, filePath, dp.stats, dp.sourceID); shouldSend {
					if _, err := dp.output.Write(result); err != nil {
						return err
					}
					
					// Update transmission stats
					if dp.stats != nil {
						dp.stats.updateLineTransmitted()
					}
				}
			}
			break
		}
		
		if err != nil {
			return err
		}
	}
	
	// Flush any buffered output
	if final := dp.processor.Flush(); len(final) > 0 {
		if _, err := dp.output.Write(final); err != nil {
			return err
		}
	}
	
	return nil
}

// GrepProcessor handles grep-style filtering
type GrepProcessor struct {
	regex    regex.Regex
	plain    bool
	noColor  bool
	hostname string
	
	// Context handling
	beforeContext int
	afterContext  int
	maxCount      int
	
	// State for context processing
	matchCount      int
	afterRemaining  int
	beforeBuffer    [][]byte
	beforeLineNums  []int
}

// NewGrepProcessor creates a new grep processor
func NewGrepProcessor(re regex.Regex, plain, noColor bool, hostname string, beforeContext, afterContext, maxCount int) *GrepProcessor {
	gp := &GrepProcessor{
		regex:         re,
		plain:         plain,
		noColor:       noColor,
		hostname:      hostname,
		beforeContext: beforeContext,
		afterContext:  afterContext,
		maxCount:      maxCount,
		matchCount:    0,
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

func (gp *GrepProcessor) ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) ([]byte, bool) {
	isMatch := gp.regex.Match(line)
	
	
	// Handle lines that don't match the regex
	if !isMatch {
		// Handle after context lines (only for non-matching lines)
		if gp.afterRemaining > 0 {
			gp.afterRemaining--
			// Send this line as context
			if stats != nil {
				stats.updateLineMatched() // Count context lines as transmitted
			}
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
	
	// Update stats for matched line
	if stats != nil {
		stats.updateLineMatched()
	}
	
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
		result := make([]byte, len(line)+1)
		copy(result, line)
		result[len(line)] = '\n'
		return result
	}
	
	// Format exactly like original basehandler.go for non-plain mode
	// REMOTE|{hostname}|{TransmittedPerc}|{Count}|{SourceID}|{Content}¬
	var transmittedPerc int
	var count uint64
	if stats != nil {
		transmittedPerc = stats.transmittedPerc()
		count = stats.totalLineCount()
	}
	
	result := make([]byte, 0, len(line)+200)
	result = append(result, "REMOTE"...)
	result = append(result, protocol.FieldDelimiter...)
	result = append(result, gp.hostname...)
	result = append(result, protocol.FieldDelimiter...)
	result = append(result, fmt.Sprintf("%3d", transmittedPerc)...)
	result = append(result, protocol.FieldDelimiter...)
	result = append(result, fmt.Sprintf("%v", count)...)
	result = append(result, protocol.FieldDelimiter...)
	result = append(result, sourceID...)
	result = append(result, protocol.FieldDelimiter...)
	result = append(result, line...)
	result = append(result, '\n')
	
	return result
}

// CatProcessor handles cat-style output
type CatProcessor struct {
	plain    bool
	noColor  bool
	hostname string
	isFirstLine bool
}

// NewCatProcessor creates a new cat processor
func NewCatProcessor(plain, noColor bool, hostname string) *CatProcessor {
	return &CatProcessor{
		plain:    plain,
		noColor:  noColor,
		hostname: hostname,
		isFirstLine: true,
	}
}

func (cp *CatProcessor) Initialize(ctx context.Context) error {
	return nil
}

func (cp *CatProcessor) Cleanup() error {
	return nil
}

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
	
	// Apply ANSI color formatting if not in plain mode and not noColor mode
	if !cp.plain && !cp.noColor {
		colorized := brush.Colorfy(protocolLine)
		
		// Add color reset prefix for all lines except the first
		var result []byte
		if cp.isFirstLine {
			cp.isFirstLine = false
			result = make([]byte, len(colorized)+1)
			copy(result, colorized)
			result[len(colorized)] = '\n'
		} else {
			// Add color reset prefix: [39m[49m[49m[39m
			colorResetPrefix := "\x1b[39m\x1b[49m\x1b[49m\x1b[39m"
			result = make([]byte, len(colorResetPrefix)+len(colorized)+1)
			copy(result, colorResetPrefix)
			copy(result[len(colorResetPrefix):], colorized)
			result[len(colorResetPrefix)+len(colorized)] = '\n'
		}
		return result, true
	}
	
	// No color formatting
	result := make([]byte, len(protocolLine)+1)
	copy(result, protocolLine)
	result[len(protocolLine)] = '\n'
	
	return result, true
}

func (cp *CatProcessor) Flush() []byte {
	// Add final color reset line to match original behavior (no trailing newline)
	// Only in non-plain mode with colors enabled
	if !cp.plain && !cp.noColor {
		return []byte("\x1b[39m\x1b[49m\x1b[49m\x1b[39m")
	}
	return nil
}

// TailProcessor handles tail-style output with following capability
type TailProcessor struct {
	regex      regex.Regex
	plain      bool
	noColor    bool
	hostname   string
	seekEOF    bool
	follow     bool
	lastLines  int
	buffer     [][]byte // For -n functionality
}

// NewTailProcessor creates a new tail processor
func NewTailProcessor(re regex.Regex, plain, noColor bool, hostname string, seekEOF, follow bool, lastLines int) *TailProcessor {
	return &TailProcessor{
		regex:     re,
		plain:     plain,
		noColor:   noColor,
		hostname:  hostname,
		seekEOF:   seekEOF,
		follow:    follow,
		lastLines: lastLines,
		buffer:    make([][]byte, 0, lastLines),
	}
}

func (tp *TailProcessor) Initialize(ctx context.Context) error {
	return nil
}

func (tp *TailProcessor) Cleanup() error {
	return nil
}

func (tp *TailProcessor) ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) ([]byte, bool) {
	// Apply regex filter if specified
	if !tp.regex.Match(line) {
		return nil, false
	}
	
	// Handle -n flag (show last N lines)
	if tp.lastLines > 0 && !tp.follow {
		// Buffer lines for later output
		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)
		
		if len(tp.buffer) >= tp.lastLines {
			// Remove oldest line
			copy(tp.buffer, tp.buffer[1:])
			tp.buffer[len(tp.buffer)-1] = lineCopy
		} else {
			tp.buffer = append(tp.buffer, lineCopy)
		}
		return nil, false // Don't send until flush
	}
	
	// Regular tailing mode - send matching lines immediately
	return tp.formatLine(line, lineNum, filePath), true
}

func (tp *TailProcessor) formatLine(line []byte, lineNum int, filePath string) []byte {
	if tp.plain {
		result := make([]byte, len(line)+1)
		copy(result, line)
		result[len(line)] = '\n'
		return result
	}
	
	// Format with hostname, filepath, and line number
	formatted := make([]byte, 0, len(line)+100)
	formatted = append(formatted, tp.hostname...)
	formatted = append(formatted, '|')
	formatted = append(formatted, filePath...)
	formatted = append(formatted, '|')
	
	// Add line number
	lineNumStr := make([]byte, 0, 10)
	lineNumStr = appendInt(lineNumStr, lineNum)
	formatted = append(formatted, lineNumStr...)
	formatted = append(formatted, '|')
	formatted = append(formatted, line...)
	formatted = append(formatted, '\n')
	
	return formatted
}

func (tp *TailProcessor) Flush() []byte {
	// For -n flag, return buffered lines
	if tp.lastLines > 0 && len(tp.buffer) > 0 {
		var result []byte
		for i, line := range tp.buffer {
			formatted := tp.formatLine(line, i+1, "")
			result = append(result, formatted...)
		}
		return result
	}
	return nil
}

// MapProcessor handles MapReduce-style aggregation
type MapProcessor struct {
	plain      bool
	hostname   string
	aggregator interface{} // Will be set to actual aggregator from mapr package
	buffer     []byte
}

// NewMapProcessor creates a new map processor
func NewMapProcessor(plain bool, hostname string) *MapProcessor {
	return &MapProcessor{
		plain:    plain,
		hostname: hostname,
		buffer:   make([]byte, 0, 1024*1024), // 1MB buffer for aggregation
	}
}

func (mp *MapProcessor) Initialize(ctx context.Context) error {
	// TODO: Initialize MapReduce aggregator when implementing
	return nil
}

func (mp *MapProcessor) Cleanup() error {
	return nil
}

func (mp *MapProcessor) ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) ([]byte, bool) {
	// For MapReduce, we accumulate lines and process in batch
	// TODO: Pass line to aggregator when implementing MapReduce integration
	return nil, false // No immediate output for MapReduce
}

func (mp *MapProcessor) Flush() []byte {
	// TODO: Return aggregated results from MapReduce processor
	// For now, return empty to maintain interface
	return nil
}

// Helper function to append integer to byte slice
func appendInt(dst []byte, i int) []byte {
	if i == 0 {
		return append(dst, '0')
	}
	
	// Convert to string and append
	str := make([]byte, 0, 10)
	for i > 0 {
		str = append(str, byte('0'+i%10))
		i /= 10
	}
	
	// Reverse the string
	for i := 0; i < len(str)/2; i++ {
		str[i], str[len(str)-1-i] = str[len(str)-1-i], str[i]
	}
	
	return append(dst, str...)
}