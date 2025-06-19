package fs

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/constants"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

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
	initialBufSize := constants.InitialBufferSize
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
		case <-time.After(constants.ProcessorTimeoutDuration):
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