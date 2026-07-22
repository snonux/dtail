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

	// Compute the local-context predicate once. When no context is requested we
	// can take the zero-copy fast path (match before copy); when it is, every
	// line must be buffered so surrounding before/after lines remain available.
	hasContext := ltx.Has()

	// Use a scanner for efficient line reading
	scanner := bufio.NewScanner(reader)

	// Get a buffer from the pool instead of allocating a new one
	bufPtr := pool.GetScannerBuffer()
	buf := *bufPtr
	maxTokenSize := 1024 * 1024 // 1MB max token size
	scanner.Buffer(buf, maxTokenSize)

	// Ensure we return the buffer to the pool when done
	defer pool.PutScannerBuffer(bufPtr)

	// Use the cancellation-aware split function so long-line warnings can be
	// abandoned if the caller cancels while the reader is blocked.
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		return f.scanLinesWithMaxLength(ctx, data, atEOF)
	})

	for scanner.Scan() {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Check for file truncation. The periodicTruncateCheck goroutine
		// (started in StartWithProcessorOptimized) already ticks every 3s and
		// signals on the unbuffered truncate channel, so this non-blocking
		// receive only re-stats the file on that cadence. Keeping the timing in
		// the goroutine lets the per-line cost be a single atomic load on an
		// empty channel (Go's non-blocking chanrecv fast path) instead of a
		// per-line time.Since/runtime.nanotime call, which profiling showed as
		// 10-18% of serverless dcat/dgrep CPU. This path is non-follow
		// (cat/grep) only; follow mode uses tailWithProcessorOptimized, which
		// has its own truncate handling.
		select {
		case <-truncate:
			if isTruncated, err := f.truncated(fd); isTruncated {
				return err
			}
		default:
		}

		// Get the line data. scanner.Bytes() is only valid until the next
		// Scan(); we must not retain it across iterations.
		lineData := scanner.Bytes()
		f.updatePosition()

		if !hasContext {
			// Fast path: run the regex on the scanner's slice directly and only
			// copy into a pooled buffer on a match. At low hit rates this skips
			// the pool.Get + copy for the discarded (non-matching) lines.
			if err := filterProcessor.ProcessFilteredRaw(lineData); err != nil {
				if isEarlyStop(err) {
					return nil
				}
				return err
			}
			continue
		}

		// Local-context path: buffer every line (before/after context needs the
		// surrounding non-matching lines), so copy into a pooled buffer first.
		lineBuf := pool.BytesBuffer.Get().(*bytes.Buffer)
		lineBuf.Write(lineData)
		if err := filterProcessor.ProcessFilteredLine(lineBuf); err != nil {
			if isEarlyStop(err) {
				return nil
			}
			return err
		}
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

// isEarlyStop reports whether err is the io.EOF sentinel that filteringProcessor
// returns from processWithContext once a max-count (-m/-max) limit is reached.
// This is a NORMAL early stop, not a genuine I/O error: bufio.Scanner signals
// real end-of-input via Scan()==false and never returns io.EOF from
// ProcessFiltered*, so any io.EOF bubbling up from the filter can only be the
// max-count sentinel. The byte-by-byte path (readWithProcessor) swallows it and
// returns nil; the optimized path must do the same, otherwise the sentinel leaks
// out to the caller and is logged as a spurious SERVER|...|ERROR|...|EOF line.
// Real (non-EOF) processor errors are left untouched so they still surface.
//
// Bare equality (== io.EOF) is intentional and must NOT become errors.Is: the
// sentinel is returned bare by processWithContext, so exact identity matches it
// precisely. A WRAPPED io.EOF, by contrast, can only originate from a genuine
// downstream failure (e.g. an ssh channel Write after the peer closed), which we
// deliberately do NOT want to mistake for a clean early stop.
func isEarlyStop(err error) bool {
	return err == io.EOF
}

// scanLinesPreserveEndings is a custom split function that preserves original line endings
// and respects MaxLineLength
func (f *readFile) scanLinesPreserveEndings(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	maxLineLen := f.lineLimit()

	// Look for a newline
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		// Check if the line before the newline exceeds max length
		if i > maxLineLen {
			// Line is too long, split it silently at maxLineLen
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
			// Even at EOF, respect max line length (split silently)
			return maxLineLen, data[0:maxLineLen], nil
		}
		return len(data), data, nil
	}

	// If the line is too long, split it
	if len(data) >= maxLineLen {
		// Return a chunk up to MaxLineLength (split silently)
		return maxLineLen, data[0:maxLineLen], nil
	}

	// Request more data
	return 0, nil, nil
}

// scanLinesWithMaxLength is a custom split function for bufio.Scanner that respects MaxLineLength.
// It is kept context-aware so long-line warnings can still be dropped when the reader is canceled.
func (f *readFile) scanLinesWithMaxLength(ctx context.Context, data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	maxLineLen := f.lineLimit()

	// Look for a newline
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		// Check if the line before the newline exceeds max length
		if i > maxLineLen {
			// Line is too long, split it at maxLineLen
			if !f.warnAboutLongLine(ctx) {
				return 0, nil, ctx.Err()
			}
			return maxLineLen, data[0:maxLineLen], nil
		}
		// We have a full line within the limit
		f.warnedAboutLongLine = false // Reset warning for next long line sequence
		return i + 1, data[0 : i+1], nil
	}

	// If we're at EOF, we have a final, non-terminated line
	if atEOF {
		if len(data) > maxLineLen {
			// Even at EOF, respect max line length
			if !f.warnAboutLongLine(ctx) {
				return 0, nil, ctx.Err()
			}
			return maxLineLen, data[0:maxLineLen], nil
		}
		return len(data), data, nil
	}

	// If the line is too long, split it
	if len(data) >= maxLineLen {
		// Warn about long line (only once)
		if !f.warnAboutLongLine(ctx) {
			return 0, nil, ctx.Err()
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

	// Create a cancelable context for the truncate check goroutine
	truncateCtx, cancelTruncate := context.WithCancel(ctx)
	defer cancelTruncate()

	truncate := make(chan struct{})

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

	// Compute the local-context predicate once (see readWithProcessorOptimized):
	// without context we take the zero-copy match-before-copy fast path.
	hasContext := ltx.Has()

	// Buffer for partial lines
	partialLine := pool.BytesBuffer.Get().(*bytes.Buffer)
	defer pool.RecycleBytesBuffer(partialLine)

	// Get a buffer from the pool for reading
	bufPtr := pool.GetMediumBuffer()
	defer pool.PutMediumBuffer(bufPtr)

	// processPartialLine advances the line position and hands the currently
	// accumulated partialLine to the filter. Without local context it takes the
	// zero-copy fast path (match on partialLine.Bytes(), copy only on a match);
	// with context it copies into a pooled buffer so surrounding lines stay
	// buffered. partialLine is owned by this loop (reset after each call), so the
	// fast path never retains its slice past the copy-on-match.
	processPartialLine := func() error {
		f.updatePosition()
		if !hasContext {
			return filterProcessor.ProcessFilteredRaw(partialLine.Bytes())
		}
		lineBuf := pool.BytesBuffer.Get().(*bytes.Buffer)
		lineBuf.Write(partialLine.Bytes())
		return filterProcessor.ProcessFilteredLine(lineBuf)
	}

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
						if err := processPartialLine(); err != nil {
							// Max-count early stop is a clean stop, not an error
							// (see isEarlyStop); mirror the byte-by-byte path.
							if isEarlyStop(err) {
								return nil
							}
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
					if partialLine.Len() >= f.lineLimit() {
						if !f.warnAboutLongLine(ctx) {
							return nil
						}

						// Process the partial line
						if err := processPartialLine(); err != nil {
							if isEarlyStop(err) {
								return nil
							}
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

			waitForMoreData := true

			// EOF handling
			select {
			case <-ctx.Done():
				return nil
			case <-truncate:
				if isTruncated, err := f.truncated(fd); isTruncated {
					return err
				}
				waitForMoreData = false
			default:
			}

			if waitForMoreData && !ctxutil.Sleep(ctx, 100*time.Millisecond) {
				return nil
			}
		}

		// Check for cancellation
		select {
		case <-ctx.Done():
			// Process any remaining partial line
			if partialLine.Len() > 0 {
				if err := processPartialLine(); err != nil && !isEarlyStop(err) {
					return err
				}
			}
			return nil
		default:
		}
	}
}
