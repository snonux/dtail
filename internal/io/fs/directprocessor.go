package fs

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/constants"
	"github.com/mimecast/dtail/internal/lcontext"
)

// LineProcessor interface for direct line-by-line processing
type LineProcessor interface {
	ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) (result []byte, shouldSend bool)
	Flush() []byte // For any buffered output (e.g., MapReduce)
	Initialize(ctx context.Context) error
	Cleanup() error
}

// LineWriter interface for writers that need sourceID information
type LineWriter interface {
	io.Writer
	WriteLine(data []byte, sourceID string, stats interface{}) error
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
	// Check if we need to preserve line endings (for any processor in plain mode)
	needsLineEndingPreservation := false
	
	if catProcessor, ok := dp.processor.(*CatProcessor); ok && catProcessor.plain {
		needsLineEndingPreservation = true
	} else if grepProcessor, ok := dp.processor.(*GrepProcessor); ok && grepProcessor.plain {
		needsLineEndingPreservation = true
	}
	// Note: MapProcessor doesn't have a plain mode that requires line ending preservation
	
	if needsLineEndingPreservation {
		return dp.processReaderPreservingLineEndings(ctx, reader, filePath)
	}

	scanner := bufio.NewScanner(reader)

	// Set buffer size respecting MaxLineLength configuration
	maxLineLength := config.Server.MaxLineLength
	initialBufSize := constants.InitialBufferSize
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
			// Check if output writer supports sourceID (for proper protocol formatting)
			if lineWriter, ok := dp.output.(LineWriter); ok {
				if err := lineWriter.WriteLine(result, dp.sourceID, dp.stats); err != nil {
					return err
				}
			} else {
				if _, err := dp.output.Write(result); err != nil {
					return err
				}
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

				// Extract the line including its original line ending (CRLF or LF)
				line := data[:lfIndex+1] // Include the LF (and CR if present before it)
				data = data[lfIndex+1:]  // Continue with remaining data

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

						// Process the chunk
						if result, shouldSend := dp.processor.ProcessLine(chunk, lineNum, filePath, dp.stats, dp.sourceID); shouldSend {
							// Update position stats only for lines that will be sent
							if dp.stats != nil {
								dp.stats.updatePosition()
								dp.stats.updateLineMatched()
							}
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

					// Process line directly (line includes original line ending)
					if result, shouldSend := dp.processor.ProcessLine(line, lineNum, filePath, dp.stats, dp.sourceID); shouldSend {
						// Update position stats only for lines that will be sent
						if dp.stats != nil {
							dp.stats.updatePosition()
							dp.stats.updateLineMatched()
						}
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
						// DEBUG: Log stats
						// fmt.Printf("DEBUG: After transmission - matchCount=%d, transmitCount=%d, percentage=%d\n", 
						//     dp.stats.matchCount, dp.stats.transmitCount, dp.stats.transmittedPerc())
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
		case <-time.After(constants.ProcessorTimeoutDuration):
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
