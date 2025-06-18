package fs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/mapr/logformat"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/regex"
)

// LineProcessor interface for direct line-by-line processing
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
							// Note: we don't have server messages channel in direct processing mode
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
	formatted := tp.formatLine(line, lineNum, filePath)
	return formatted, true
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

// FollowingTailProcessor extends DirectProcessor with file following capability
type FollowingTailProcessor struct {
	*DirectProcessor
	tailProcessor *TailProcessor
}

// NewFollowingTailProcessor creates a processor that can follow files
func NewFollowingTailProcessor(processor *TailProcessor, output io.Writer, globID string, ltx lcontext.LContext) *FollowingTailProcessor {
	dp := NewDirectProcessor(processor, output, globID, ltx)
	return &FollowingTailProcessor{
		DirectProcessor: dp,
		tailProcessor:   processor,
	}
}

// ProcessFileWithFollowing processes a file with following capability
func (ftp *FollowingTailProcessor) ProcessFileWithFollowing(ctx context.Context, filePath string) error {
	if !ftp.tailProcessor.follow {
		// No following required, use regular processing
		return ftp.ProcessFile(ctx, filePath)
	}

	// Implement file following logic
	return ftp.followFile(ctx, filePath)
}

func (ftp *FollowingTailProcessor) followFile(ctx context.Context, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Initialize processor
	if err := ftp.processor.Initialize(ctx); err != nil {
		return err
	}
	defer ftp.processor.Cleanup()

	// If seekEOF is true, seek to end first
	if ftp.tailProcessor.seekEOF {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			return err
		}
	}

	return ftp.followReader(ctx, file, filePath)
}

func (ftp *FollowingTailProcessor) followReader(ctx context.Context, file *os.File, filePath string) error {
	// Set buffer size respecting MaxLineLength configuration
	maxLineLength := config.Server.MaxLineLength
	initialBufSize := 64 * 1024
	if maxLineLength < initialBufSize {
		initialBufSize = maxLineLength
	}
	
	lineNum := 0
	lastPosition := int64(0)
	readBuffer := make([]byte, initialBufSize)
	lineBuffer := make([]byte, 0, initialBufSize)
	
	// Get initial position
	if pos, err := file.Seek(0, io.SeekCurrent); err == nil {
		lastPosition = pos
	}
	
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		
		// Check if file has grown
		if stat, err := file.Stat(); err == nil {
			if stat.Size() > lastPosition {
				// Read new content
				n, err := file.Read(readBuffer)
				if err != nil && err != io.EOF {
					return err
				}
				
				if n > 0 {
					// Process the data, looking for complete lines
					for i := 0; i < n; i++ {
						b := readBuffer[i]
						if b == '\n' {
							// Found a complete line
							lineNum++
							line := make([]byte, len(lineBuffer))
							copy(line, lineBuffer)
							
							// Update position stats
							if ftp.stats != nil {
								ftp.stats.updatePosition()
							}
							
							// Process line directly  
							if result, shouldSend := ftp.processor.ProcessLine(line, lineNum, filePath, ftp.stats, ftp.sourceID); shouldSend {
								if _, err := ftp.output.Write(result); err != nil {
									return err
								}
								
								// Update transmission stats
								if ftp.stats != nil {
									ftp.stats.updateLineTransmitted()
								}
							}
							
							// Reset line buffer for next line
							lineBuffer = lineBuffer[:0]
						} else {
							// Add byte to current line
							lineBuffer = append(lineBuffer, b)
						}
					}
					
					// Update last position
					if pos, err := file.Seek(0, io.SeekCurrent); err == nil {
						lastPosition = pos
					}
					
					continue
				}
			}
		}
		
		// No more content available, check if file was truncated/rotated
		if ftp.checkFileRotation(file, filePath, &lastPosition) {
			// File was rotated, reopen and continue
			file.Close()
			var err error
			file, err = os.Open(filePath)
			if err != nil {
				return err
			}
			defer file.Close()
			
			lastPosition = 0
			lineBuffer = lineBuffer[:0]
			continue
		}
		
		// Wait a bit before checking for new content
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
			// Continue the loop to check for new content
		}
	}
}

func (ftp *FollowingTailProcessor) checkFileRotation(file *os.File, filePath string, lastPosition *int64) bool {
	// Get current file info
	currentInfo, err := file.Stat()
	if err != nil {
		return false
	}
	
	// Get file info by path
	pathInfo, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	
	// Check if file was truncated (size is smaller than our position)
	if pathInfo.Size() < *lastPosition {
		return true
	}
	
	// Check if file was rotated (different inode/device)
	if !os.SameFile(currentInfo, pathInfo) {
		return true
	}
	
	return false
}

// MapProcessor handles MapReduce-style aggregation
type MapProcessor struct {
	plain           bool
	hostname        string
	query           *mapr.Query
	parser          logformat.Parser
	groupSet        *mapr.GroupSet
	buffer          []byte
	output          io.Writer
	lastSerialized  time.Time
	serializeFunc   func(groupSet *mapr.GroupSet)
}

// NewMapProcessor creates a new map processor
func NewMapProcessor(plain bool, hostname string, queryStr string, output io.Writer) (*MapProcessor, error) {
	query, err := mapr.NewQuery(queryStr)
	if err != nil {
		return nil, err
	}

	var parserName string
	switch query.LogFormat {
	case "":
		parserName = config.Server.MapreduceLogFormat
		if query.Table == "" {
			parserName = "generic"
		}
	default:
		parserName = query.LogFormat
	}

	logParser, err := logformat.NewParser(parserName, query)
	if err != nil {
		dlog.Server.Error("Could not create log format parser. Falling back to 'generic'", err)
		if logParser, err = logformat.NewParser("generic", query); err != nil {
			return nil, fmt.Errorf("could not create log format parser: %w", err)
		}
	}

	mp := &MapProcessor{
		plain:          plain,
		hostname:       hostname,
		query:          query,
		parser:         logParser,
		groupSet:       mapr.NewGroupSet(),
		buffer:         make([]byte, 0, 1024*1024), // 1MB buffer for aggregation
		output:         output,
		lastSerialized: time.Now(),
	}
	
	// Set up serialization function
	mp.serializeFunc = mp.defaultSerializeFunc
	
	return mp, nil
}

// SetSerializeFunc allows custom serialization (for testing or different output formats)
func (mp *MapProcessor) SetSerializeFunc(fn func(groupSet *mapr.GroupSet)) {
	mp.serializeFunc = fn
}

func (mp *MapProcessor) Initialize(ctx context.Context) error {
	return nil
}

func (mp *MapProcessor) Cleanup() error {
	return nil
}

func (mp *MapProcessor) ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) ([]byte, bool) {
	// Convert line to string and parse fields
	maprLine := strings.TrimSpace(string(line))
	
	fields, err := mp.parser.MakeFields(maprLine)
	if err != nil {
		// Should fields be ignored anyway?
		if err != logformat.ErrIgnoreFields {
			dlog.Server.Error("Error parsing line for MapReduce", err)
		}
		return nil, false
	}
	
	// Apply WHERE clause filter
	if !mp.query.WhereClause(fields) {
		return nil, false
	}
	
	// Apply SET clause (add additional fields)
	if len(mp.query.Set) > 0 {
		if err := mp.query.SetClause(fields); err != nil {
			dlog.Server.Error("Error applying SET clause", err)
			return nil, false
		}
	}
	
	// Aggregate the fields
	mp.aggregateFields(fields)
	
	// Check if we should serialize results periodically (every 5 seconds by default)
	now := time.Now()
	if now.Sub(mp.lastSerialized) >= mp.query.Interval {
		mp.periodicSerialize()
		mp.lastSerialized = now
	}
	
	return nil, false // No immediate output for MapReduce - output happens periodically
}

func (mp *MapProcessor) aggregateFields(fields map[string]string) {
	var sb strings.Builder
	for i, field := range mp.query.GroupBy {
		if i > 0 {
			sb.WriteString(protocol.AggregateGroupKeyCombinator)
		}
		if val, ok := fields[field]; ok {
			sb.WriteString(val)
		}
	}
	groupKey := sb.String()
	set := mp.groupSet.GetSet(groupKey)

	var addedSample bool
	for _, sc := range mp.query.Select {
		if val, ok := fields[sc.Field]; ok {
			if err := set.Aggregate(sc.FieldStorage, sc.Operation, val, false); err != nil {
				dlog.Server.Error("Error aggregating field", err)
				continue
			}
			addedSample = true
		}
	}

	if addedSample {
		set.Samples++
	}
}

// periodicSerialize sends current aggregation results and resets the group set
func (mp *MapProcessor) periodicSerialize() {
	if mp.serializeFunc != nil {
		mp.serializeFunc(mp.groupSet)
	}
	// Reset group set for next interval
	mp.groupSet = mapr.NewGroupSet()
}

// defaultSerializeFunc implements the default serialization behavior
func (mp *MapProcessor) defaultSerializeFunc(groupSet *mapr.GroupSet) {
	// Use a channel to collect serialized data
	ch := make(chan string, 100)
	done := make(chan struct{})
	
	go func() {
		defer close(done)
		for msg := range ch {
			// Format as protocol message: A|{serialized_data}¬
			var output strings.Builder
			output.WriteString("A")
			output.WriteString(protocol.FieldDelimiter)
			output.WriteString(msg)
			output.WriteByte(protocol.MessageDelimiter)
			
			// Write to output immediately
			if mp.output != nil {
				mp.output.Write([]byte(output.String()))
			}
		}
	}()
	
	// Serialize the group set
	ctx := context.Background()
	groupSet.Serialize(ctx, ch)
	close(ch)
	<-done
}

func (mp *MapProcessor) Flush() []byte {
	// Final flush - serialize any remaining data
	if mp.serializeFunc != nil {
		mp.serializeFunc(mp.groupSet)
	}
	return nil // Output is handled by serializeFunc
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

// ProcessFileWithTailing processes a file with tailing capability
func (dp *DirectProcessor) ProcessFileWithTailing(ctx context.Context, filePath string) error {
	// Use the same logic as FollowingTailProcessor but with our DirectProcessor
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// First, process existing content
	if err := dp.ProcessReader(ctx, file, filePath); err != nil {
		return err
	}

	// Then follow the file for new content
	return dp.followFile(ctx, filePath)
}

// followFile implements file following logic similar to FollowingTailProcessor
func (dp *DirectProcessor) followFile(ctx context.Context, filePath string) error {
	// Track our current position in the file
	var lastSize int64
	
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
			// Check if file has grown
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				continue
			}
			
			currentSize := fileInfo.Size()
			if currentSize > lastSize {
				// File has new content, read it
				file, err := os.Open(filePath)
				if err != nil {
					continue
				}
				
				// Seek to where we left off
				if _, err := file.Seek(lastSize, 0); err != nil {
					file.Close()
					continue
				}
				
				// Process new content
				if err := dp.processNewContent(ctx, file, filePath); err != nil {
					file.Close()
					continue
				}
				
				lastSize = currentSize
				file.Close()
			}
		}
	}
}

// processNewContent processes new content that was added to the file
func (dp *DirectProcessor) processNewContent(ctx context.Context, file *os.File, filePath string) error {
	scanner := bufio.NewScanner(file)
	
	// Start line counting from where we left off (simplified approach)
	lineNum := 1
	
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		
		lineBuf := scanner.Bytes()
		if result, shouldSend := dp.processor.ProcessLine(lineBuf, lineNum, filePath, dp.stats, dp.sourceID); shouldSend {
			if _, err := dp.output.Write(result); err != nil {
				return err
			}
			
			// Update transmission stats
			if dp.stats != nil {
				dp.stats.updateLineTransmitted()
			}
		}
		lineNum++
		
		// Update position stats
		if dp.stats != nil {
			dp.stats.updatePosition()
		}
	}
	
	return scanner.Err()
}