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

// StartWithProcessor starts reading a log file using a LineProcessor for handling lines.
// This is a channel-less implementation for better performance.
func (f *readFile) StartWithProcessor(ctx context.Context, ltx lcontext.LContext,
	processor line.Processor, re regex.Regex) error {

	reader, fd, err := f.makeReader()
	if fd != nil {
		defer fd.Close()
	}
	if err != nil {
		return err
	}

	truncate := make(chan struct{})
	defer close(truncate)

	go f.periodicTruncateCheck(ctx, truncate)

	// Process file with direct callbacks instead of channels
	err = f.readWithProcessor(ctx, fd, reader, truncate, ltx, processor, re)
	
	// Ensure any buffered data is flushed
	if flushErr := processor.Flush(); flushErr != nil && err == nil {
		err = flushErr
	}

	return err
}

// readWithProcessor reads from the file and processes lines directly without channels
func (f *readFile) readWithProcessor(ctx context.Context, fd *os.File, reader *bufio.Reader,
	truncate <-chan struct{}, ltx lcontext.LContext, processor line.Processor, re regex.Regex) error {

	var offset uint64
	message := pool.BytesBuffer.Get().(*bytes.Buffer)
	defer pool.RecycleBytesBuffer(message)

	// Create a line filter processor that wraps the given processor
	filterProcessor := &filteringProcessor{
		processor: processor,
		re:        re,
		ltx:       ltx,
		stats:     &f.stats,
		globID:    f.globID,
	}

	for {
		b, err := reader.ReadByte()
		if err != nil {
			status, err := f.handleReadErrorProcessor(ctx, err, fd, truncate, message, filterProcessor)
			if abortReading == status {
				return err
			}
			time.Sleep(time.Millisecond * 100)
			continue
		}

		offset++
		message.WriteByte(b)

		status := f.handleReadByteProcessor(ctx, b, message, filterProcessor)
		if status == abortReading {
			return nil
		}
		if status == continueReading {
			// Get a new buffer for the next line
			message = pool.BytesBuffer.Get().(*bytes.Buffer)
		}
	}
}

// handleReadByteProcessor processes a byte read from the file
func (f *readFile) handleReadByteProcessor(ctx context.Context, b byte,
	message *bytes.Buffer, processor *filteringProcessor) readStatus {

	switch b {
	case '\n':
		// Process the complete line
		f.updatePosition()
		if err := processor.ProcessFilteredLine(message); err != nil {
			return abortReading
		}
		
		f.warnedAboutLongLine = false
		return continueReading

	default:
		if message.Len() >= config.Server.MaxLineLength {
			if !f.warnedAboutLongLine {
				f.serverMessages <- dlog.Common.Warn(f.filePath,
					"Long log line, splitting into multiple lines") + "\n"
				f.warnedAboutLongLine = true
			}
			// Force a line break
			message.WriteByte('\n')
			
			// Process the line
			f.updatePosition()
			if err := processor.ProcessFilteredLine(message); err != nil {
				return abortReading
			}
			return continueReading
		}
	}

	return nothing
}

// handleReadErrorProcessor handles read errors in processor mode
func (f *readFile) handleReadErrorProcessor(ctx context.Context, err error, fd *os.File,
	truncate <-chan struct{}, message *bytes.Buffer, processor *filteringProcessor) (readStatus, error) {

	if err != io.EOF {
		return abortReading, err
	}

	select {
	case <-truncate:
		if isTruncated, err := f.truncated(fd); isTruncated {
			return abortReading, err
		}
	case <-ctx.Done():
		return abortReading, nil
	default:
	}

	if !f.seekEOF {
		dlog.Common.Info(f.FilePath(), "End of file reached")
		if len(message.Bytes()) > 0 {
			// Process the last line if it doesn't end with newline
			f.updatePosition()
			processor.ProcessFilteredLine(message)
		}
		return abortReading, nil
	}

	return nothing, nil
}

// filteringProcessor wraps a LineProcessor to add regex filtering
type filteringProcessor struct {
	processor line.Processor
	re        regex.Regex
	ltx       lcontext.LContext
	stats     *stats
	globID    string
	
	// For local context handling
	beforeBuf   []*bytes.Buffer
	afterCount  int
	maxCount    int
	maxReached  bool
}

// ProcessFilteredLine applies regex filtering before passing to the underlying processor
func (fp *filteringProcessor) ProcessFilteredLine(rawLine *bytes.Buffer) error {
	// Update stats
	lineNum := fp.stats.totalLineCount()
	
	// Simple case: no local context
	if !fp.ltx.Has() {
		if !fp.re.Match(rawLine.Bytes()) {
			fp.stats.updateLineNotMatched()
			fp.stats.updateLineNotTransmitted()
			pool.RecycleBytesBuffer(rawLine)
			return nil
		}
		
		fp.stats.updateLineMatched()
		fp.stats.updateLineTransmitted()
		
		// Process the line
		err := fp.processor.ProcessLine(rawLine, lineNum, fp.globID)
		if err != nil {
			pool.RecycleBytesBuffer(rawLine)
		}
		return err
	}
	
	// Complex case: handle local context (before/after/max)
	return fp.processWithContext(rawLine, lineNum)
}

// processWithContext handles lines when local context is enabled
func (fp *filteringProcessor) processWithContext(rawLine *bytes.Buffer, lineNum uint64) error {
	matched := fp.re.Match(rawLine.Bytes())
	
	if !matched {
		fp.stats.updateLineNotMatched()
		
		// Handle after context
		if fp.ltx.AfterContext > 0 && fp.afterCount > 0 {
			fp.afterCount--
			fp.stats.updateLineTransmitted()
			err := fp.processor.ProcessLine(rawLine, lineNum, fp.globID)
			if err != nil {
				pool.RecycleBytesBuffer(rawLine)
			}
			return err
		}
		
		// Handle before context buffer
		if fp.ltx.BeforeContext > 0 {
			// Add to before buffer
			if len(fp.beforeBuf) >= fp.ltx.BeforeContext {
				// Recycle oldest buffer
				pool.RecycleBytesBuffer(fp.beforeBuf[0])
				fp.beforeBuf = fp.beforeBuf[1:]
			}
			fp.beforeBuf = append(fp.beforeBuf, rawLine)
		} else {
			pool.RecycleBytesBuffer(rawLine)
		}
		
		fp.stats.updateLineNotTransmitted()
		return nil
	}
	
	// Line matched
	fp.stats.updateLineMatched()
	
	// Check if we've reached max count
	if fp.maxReached {
		pool.RecycleBytesBuffer(rawLine)
		return io.EOF // Stop processing
	}
	
	// Process before context
	if fp.ltx.BeforeContext > 0 && len(fp.beforeBuf) > 0 {
		for i, buf := range fp.beforeBuf {
			fp.stats.updateLineTransmitted()
			if err := fp.processor.ProcessLine(buf, lineNum-uint64(len(fp.beforeBuf)-i), fp.globID); err != nil {
				// Clean up remaining buffers
				for j := i + 1; j < len(fp.beforeBuf); j++ {
					pool.RecycleBytesBuffer(fp.beforeBuf[j])
				}
				pool.RecycleBytesBuffer(rawLine)
				return err
			}
		}
		fp.beforeBuf = fp.beforeBuf[:0] // Clear the buffer
	}
	
	// Process the matched line
	fp.stats.updateLineTransmitted()
	if err := fp.processor.ProcessLine(rawLine, lineNum, fp.globID); err != nil {
		pool.RecycleBytesBuffer(rawLine)
		return err
	}
	
	// Update max count
	if fp.ltx.MaxCount > 0 {
		fp.maxCount++
		if fp.maxCount >= fp.ltx.MaxCount {
			if fp.ltx.AfterContext == 0 {
				return io.EOF // Stop processing
			}
			fp.maxReached = true
		}
	}
	
	// Reset after context
	if fp.ltx.AfterContext > 0 {
		fp.afterCount = fp.ltx.AfterContext
	}
	
	return nil
}