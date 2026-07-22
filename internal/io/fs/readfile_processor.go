package fs

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"time"

	"github.com/mimecast/dtail/internal/ctxutil"
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

	reader, fd, decompressor, err := f.makeReader()
	if fd != nil {
		defer fd.Close()
	}
	if decompressor != nil {
		defer func() {
			if closeErr := decompressor.Close(); closeErr != nil {
				dlog.Common.Warn(f.filePath, "Unable to close compressed reader", closeErr)
			}
		}()
	}
	if err != nil {
		return err
	}

	truncateCtx, cancelTruncate := context.WithCancel(ctx)
	defer cancelTruncate()

	truncate := make(chan struct{})

	go f.periodicTruncateCheck(truncateCtx, truncate)

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
	// Use a closure so that the CURRENT value of `message` is recycled on
	// return, not the pointer captured at defer-registration time. Downstream
	// code paths that take ownership of the buffer set `message = nil` before
	// reassigning or returning, preventing a double-recycle.
	defer func() {
		if message != nil {
			pool.RecycleBytesBuffer(message)
		}
	}()

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
			// handleReadErrorProcessor may hand `message` to ProcessFilteredLine
			// (which takes ownership); in that case it sets *messagePtr = nil so
			// the caller's defer does not recycle an already-recycled buffer.
			status, err := f.handleReadErrorProcessor(ctx, err, fd, truncate, &message, filterProcessor)
			if abortReading == status {
				return err
			}
			if !ctxutil.Sleep(ctx, 100*time.Millisecond) {
				return nil
			}
			continue
		}

		offset++
		message.WriteByte(b)

		status := f.handleReadByteProcessor(ctx, b, message, filterProcessor)
		if status == abortReading {
			// ProcessFilteredLine took ownership; avoid defer double-recycle.
			message = nil
			return nil
		}
		if status == continueReading {
			// Previous buffer was consumed by ProcessFilteredLine; acquire a fresh one.
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
		if message.Len() >= f.lineLimit() {
			if !f.warnAboutLongLine(ctx) {
				return abortReading
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

// handleReadErrorProcessor handles read errors in processor mode. When it hands
// the buffer to ProcessFilteredLine it nils out *messagePtr, signalling to the
// caller that ownership has been transferred downstream.
func (f *readFile) handleReadErrorProcessor(ctx context.Context, err error, fd *os.File,
	truncate <-chan struct{}, messagePtr **bytes.Buffer, processor *filteringProcessor) (readStatus, error) {

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
		message := *messagePtr
		if len(message.Bytes()) > 0 {
			// Process the last line if it doesn't end with newline.
			f.updatePosition()
			*messagePtr = nil
			if processErr := processor.ProcessFilteredLine(message); processErr != nil {
				return abortReading, processErr
			}
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
	beforeBuf  []*bytes.Buffer
	afterCount int
	maxCount   int
	maxReached bool
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

		// Process the line. Per the line.Processor contract (processor.go),
		// ownership of rawLine transfers to the processor, which recycles it on
		// every return path. The only processors on the fs read path -
		// DirectLineProcessor and AggregateProcessor - recycle unconditionally,
		// even when ProcessLine returns a write error (e.g. a client disconnect /
		// broken pipe). Recycling here on error would Put the same buffer into the
		// shared pool.BytesBuffer a second time; the pool would then hand one object
		// to two Get callers whose concurrent writes race and corrupt data. So do
		// not recycle rawLine here.
		return fp.processor.ProcessLine(rawLine, lineNum, fp.globID)
	}

	// Complex case: handle local context (before/after/max)
	return fp.processWithContext(rawLine, lineNum)
}

// ProcessFilteredRaw is the zero-copy fast path for the no-local-context case.
// It runs the regex match directly on the scanner-owned byte slice and only
// acquires+fills a pooled buffer when the line actually matches. At low hit
// rates this avoids a pool.Get + copy + pool.Put for the (vast majority of)
// non-matching lines, which profiling showed as ~10-15% of serverless
// dgrep CPU (sync.Pool Get/Put + bytes.Buffer.Write).
//
// Semantics are identical to the !ltx.Has() branch of ProcessFilteredLine: the
// same regex, the same stats bookkeeping, and the same lineNum are used, so
// output is byte-identical. It MUST only be called when fp.ltx.Has() is false;
// the local-context path deliberately buffers non-matching lines (before/after
// context) and cannot skip the copy.
//
// The caller passes raw = scanner.Bytes(), which is only valid until the next
// Scan(). On a match we copy it into a pooled buffer before returning, so the
// buffer handed to the underlying processor is a stable copy and never aliases
// the scanner's transient slice.
func (fp *filteringProcessor) ProcessFilteredRaw(raw []byte) error {
	lineNum := fp.stats.totalLineCount()

	if !fp.re.Match(raw) {
		fp.stats.updateLineNotMatched()
		fp.stats.updateLineNotTransmitted()
		// No buffer was acquired, so there is nothing to recycle.
		return nil
	}

	fp.stats.updateLineMatched()
	fp.stats.updateLineTransmitted()

	// Only now, on a confirmed match, pay for the buffer and the copy.
	lineBuf := pool.BytesBuffer.Get().(*bytes.Buffer)
	lineBuf.Write(raw)

	// Ownership of lineBuf transfers to the processor, which recycles it on every
	// return path (see ProcessFilteredLine for the full rationale). Recycling here
	// on error would return the same buffer to the shared pool a second time and
	// race, so leave it to the processor.
	return fp.processor.ProcessLine(lineBuf, lineNum, fp.globID)
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
			// Ownership transfers to the processor, which recycles rawLine on every
			// return path; recycling here on error would double Put into the shared
			// pool and race (see ProcessFilteredLine).
			return fp.processor.ProcessLine(rawLine, lineNum, fp.globID)
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

	// Process the matched line. Ownership transfers to the processor, which
	// recycles rawLine on every return path; recycling here on error would double
	// Put into the shared pool and race (see ProcessFilteredLine).
	fp.stats.updateLineTransmitted()
	if err := fp.processor.ProcessLine(rawLine, lineNum, fp.globID); err != nil {
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
