package fs

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

// readWithProcessorOptimized reads from the file using buffered line reading
// instead of byte-by-byte reading for better performance
func (f *readFile) readWithProcessorOptimized(ctx context.Context, fd *os.File, reader *bufio.Reader,
	truncate <-chan struct{}, ltx lcontext.LContext, processor line.Processor, re regex.Regex) error {

	// Create a line filter processor that wraps the given processor
	filterProcessor := &filteringProcessor{
		processor: processor,
		re:        re,
		ltx:       ltx,
		stats:     &f.stats,
		globID:    f.globID,
	}

	// Use a scanner for efficient line reading
	scanner := bufio.NewScanner(reader)
	
	// Get a buffer from the pool instead of allocating a new one
	bufPtr := pool.GetScannerBuffer()
	buf := *bufPtr
	maxTokenSize := 1024 * 1024   // 1MB max token size
	scanner.Buffer(buf, maxTokenSize)
	
	// Ensure we return the buffer to the pool when done
	defer pool.PutScannerBuffer(bufPtr)
	
	// Use custom split function that preserves line endings
	scanner.Split(f.scanLinesPreserveEndings)
	
	// Track truncation checks
	lastTruncateCheck := time.Now()
	truncateCheckInterval := 3 * time.Second
	
	lineCount := 0
	for scanner.Scan() {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		
		// Check for file truncation periodically
		if time.Since(lastTruncateCheck) > truncateCheckInterval {
			select {
			case <-truncate:
				if isTruncated, err := f.truncated(fd); isTruncated {
					return err
				}
			default:
			}
			lastTruncateCheck = time.Now()
		}
		
		// Get the line data
		lineData := scanner.Bytes()
		
		// Get a buffer from the pool and copy the data
		lineBuf := pool.BytesBuffer.Get().(*bytes.Buffer)
		lineBuf.Write(lineData)
		
		// Process the line
		f.updatePosition()
		if err := filterProcessor.ProcessFilteredLine(lineBuf); err != nil {
			return err
		}
		
		lineCount++
	}
	
	// Check for scanner errors
	if err := scanner.Err(); err != nil {
		// Handle EOF specially for tailing
		if err == io.EOF && f.seekEOF {
			// For tail mode, we want to keep reading
			return nil
		}
		return err
	}
	
	return nil
}

// scanLinesPreserveEndings is a custom split function that preserves original line endings
// and respects MaxLineLength
func (f *readFile) scanLinesPreserveEndings(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	
	maxLineLen := config.Server.MaxLineLength
	
	// Look for a newline
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		// Check if the line before the newline exceeds max length
		if i > maxLineLen {
			// Line is too long, split it at maxLineLen
			// In turbo mode, we handle long lines silently
			return maxLineLen, data[0:maxLineLen], nil
		}
		
		// Line is within limit, include the line ending in the token
		// Check if there's a \r before the \n
		if i > 0 && data[i-1] == '\r' {
			// Windows line ending (\r\n) - include both in token
			return i + 1, data[0 : i+1], nil
		}
		// Unix line ending (\n) - include it in token
		return i + 1, data[0 : i+1], nil
	}
	
	// If we're at EOF, we have a final, non-terminated line
	if atEOF {
		if len(data) > maxLineLen {
			// Even at EOF, respect max line length
			// In turbo mode, we handle long lines silently
			return maxLineLen, data[0:maxLineLen], nil
		}
		return len(data), data, nil
	}
	
	// If the line is too long, split it
	if len(data) >= maxLineLen {
		// Return a chunk up to MaxLineLength
		// In turbo mode, we handle long lines silently
		return maxLineLen, data[0:maxLineLen], nil
	}
	
	// Request more data
	return 0, nil, nil
}

// scanLinesWithMaxLength is a custom split function for bufio.Scanner that respects MaxLineLength
func (f *readFile) scanLinesWithMaxLength(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	
	maxLineLen := config.Server.MaxLineLength
	
	// Look for a newline
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		// Check if the line before the newline exceeds max length
		if i > maxLineLen {
			// Line is too long, split it at maxLineLen
			if !f.warnedAboutLongLine {
				if f.serverMessages != nil {
					f.serverMessages <- dlog.Common.Warn(f.filePath,
						"Long log line, splitting into multiple lines") + "\n"
				}
				f.warnedAboutLongLine = true
			}
			return maxLineLen, data[0:maxLineLen], nil
		}
		// We have a full line within the limit
		f.warnedAboutLongLine = false // Reset warning for next long line sequence
		return i + 1, data[0:i], nil
	}
	
	// If we're at EOF, we have a final, non-terminated line
	if atEOF {
		if len(data) > maxLineLen {
			// Even at EOF, respect max line length
			if !f.warnedAboutLongLine {
				if f.serverMessages != nil {
					f.serverMessages <- dlog.Common.Warn(f.filePath,
						"Long log line, splitting into multiple lines") + "\n"
				}
				f.warnedAboutLongLine = true
			}
			return maxLineLen, data[0:maxLineLen], nil
		}
		return len(data), data, nil
	}
	
	// If the line is too long, split it
	if len(data) >= maxLineLen {
		// Warn about long line (only once)
		if !f.warnedAboutLongLine {
			f.serverMessages <- dlog.Common.Warn(f.filePath,
				"Long log line, splitting into multiple lines") + "\n"
			f.warnedAboutLongLine = true
		}
		
		// Return a chunk up to MaxLineLength
		return maxLineLen, data[0:maxLineLen], nil
	}
	
	// Request more data
	return 0, nil, nil
}

// StartWithProcessorOptimized starts reading a log file using an optimized LineProcessor implementation.
// This version uses buffered line reading instead of byte-by-byte reading.
func (f *readFile) StartWithProcessorOptimized(ctx context.Context, ltx lcontext.LContext,
	processor line.Processor, re regex.Regex) error {

	reader, fd, err := f.makeReader()
	if fd != nil {
		defer fd.Close()
	}
	if err != nil {
		return err
	}

	// Create a cancelable context for the truncate check goroutine
	truncateCtx, cancelTruncate := context.WithCancel(ctx)
	defer cancelTruncate()
	
	truncate := make(chan struct{})
	defer close(truncate)

	go f.periodicTruncateCheck(truncateCtx, truncate)

	// For tail mode, we need to handle continuous reading
	if f.seekEOF {
		return f.tailWithProcessorOptimized(ctx, fd, reader, truncate, ltx, processor, re)
	}

	// For cat/grep mode, just read once
	err = f.readWithProcessorOptimized(ctx, fd, reader, truncate, ltx, processor, re)
	
	// Ensure any buffered data is flushed
	if flushErr := processor.Flush(); flushErr != nil && err == nil {
		err = flushErr
	}

	return err
}

// tailWithProcessorOptimized handles continuous reading for tail mode
func (f *readFile) tailWithProcessorOptimized(ctx context.Context, fd *os.File, reader *bufio.Reader,
	truncate <-chan struct{}, ltx lcontext.LContext, processor line.Processor, re regex.Regex) error {

	// Create a line filter processor
	filterProcessor := &filteringProcessor{
		processor: processor,
		re:        re,
		ltx:       ltx,
		stats:     &f.stats,
		globID:    f.globID,
	}

	// Buffer for partial lines
	partialLine := pool.BytesBuffer.Get().(*bytes.Buffer)
	defer pool.RecycleBytesBuffer(partialLine)

	// Get a buffer from the pool for reading
	bufPtr := pool.GetMediumBuffer()
	defer pool.PutMediumBuffer(bufPtr)
	
	for {
		// Read available data using pooled buffer
		buf := (*bufPtr)[:cap(*bufPtr)] // Reset to full capacity
		n, err := reader.Read(buf)
		
		if n > 0 {
			// Process the data we read
			data := buf[:n]
			
			// Process complete lines
			for len(data) > 0 {
				// Find newline
				idx := bytes.IndexByte(data, '\n')
				
				if idx >= 0 {
					// Complete line found
					partialLine.Write(data[:idx])
					
					// Process the line if it's not empty
					if partialLine.Len() > 0 {
						f.updatePosition()
						lineBuf := pool.BytesBuffer.Get().(*bytes.Buffer)
						lineBuf.Write(partialLine.Bytes())
						
						if err := filterProcessor.ProcessFilteredLine(lineBuf); err != nil {
							return err
						}
					}
					
					partialLine.Reset()
					data = data[idx+1:]
					
					// Reset long line warning
					f.warnedAboutLongLine = false
				} else {
					// No newline, add to partial line
					partialLine.Write(data)
					
					// Check if line is too long
					if partialLine.Len() >= config.Server.MaxLineLength {
						if !f.warnedAboutLongLine {
							f.serverMessages <- dlog.Common.Warn(f.filePath,
								"Long log line, splitting into multiple lines") + "\n"
							f.warnedAboutLongLine = true
						}
						
						// Process the partial line
						f.updatePosition()
						lineBuf := pool.BytesBuffer.Get().(*bytes.Buffer)
						lineBuf.Write(partialLine.Bytes())
						
						if err := filterProcessor.ProcessFilteredLine(lineBuf); err != nil {
							return err
						}
						
						partialLine.Reset()
					}
					
					break
				}
			}
			
			// Flush processor periodically
			if err := processor.Flush(); err != nil {
				return err
			}
		}
		
		// Handle read errors
		if err != nil {
			if err != io.EOF {
				return err
			}
			
			// EOF handling
			select {
			case <-ctx.Done():
				return nil
			case <-truncate:
				if isTruncated, err := f.truncated(fd); isTruncated {
					return err
				}
			case <-time.After(100 * time.Millisecond):
				// Continue reading after a short delay
			}
		}
		
		// Check for cancellation
		select {
		case <-ctx.Done():
			// Process any remaining partial line
			if partialLine.Len() > 0 {
				f.updatePosition()
				lineBuf := pool.BytesBuffer.Get().(*bytes.Buffer)
				lineBuf.Write(partialLine.Bytes())
				filterProcessor.ProcessFilteredLine(lineBuf)
			}
			return nil
		default:
		}
	}
}