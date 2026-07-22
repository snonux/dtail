package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/protocol"
)

// LineWriter defines the interface for direct writing in output mode
type LineWriter interface {
	// WriteLineData writes formatted line data directly to output
	WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error
	// WriteServerMessage writes a server message
	WriteServerMessage(message string) error
	// Flush ensures all buffered data is written
	Flush() error
}

// DirectWriter implements LineWriter for direct network writing
type DirectWriter struct {
	writer     io.Writer
	hostname   string
	plain      bool
	serverless bool
	generation uint64

	// Buffering for efficiency
	writeBuf bytes.Buffer
	bufSize  int
	mutex    sync.Mutex

	// Stats
	linesWritten uint64
	bytesWritten uint64

	activeGeneration func() uint64
}

var _ LineWriter = (*DirectWriter)(nil)

// NewDirectWriter creates a new output writer
func NewDirectWriter(writer io.Writer, hostname string, plain, serverless bool) *DirectWriter {
	return &DirectWriter{
		writer:     writer,
		hostname:   hostname,
		plain:      plain,
		serverless: serverless,
		bufSize:    64 * 1024, // 64KB buffer
	}
}

// NewGeneratedDirectWriter creates a DirectWriter bound to a session generation.
func NewGeneratedDirectWriter(writer io.Writer, hostname string, plain, serverless bool, generation uint64, activeGeneration func() uint64) *DirectWriter {
	w := NewDirectWriter(writer, hostname, plain, serverless)
	w.generation = generation
	w.activeGeneration = activeGeneration
	return w
}

// WriteLineData writes formatted line data directly to output.
// Dispatches to serverless or network mode handlers based on configuration.
func (w *DirectWriter) WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error {
	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		return nil
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.serverless {
		return w.writeServerlessLine(lineContent, lineNum, sourceID)
	}
	return w.writeNetworkLine(lineContent, lineNum, sourceID)
}

// writeServerlessLine handles serverless mode output with buffered writes.
// Supports both plain and colored output modes. Must be called with mutex held.
func (w *DirectWriter) writeServerlessLine(lineContent []byte, lineNum uint64, sourceID string) error {
	// writeBuf accumulates lines until bufSize before flushing, so record its
	// length before appending: the bytesWritten stat must count only this
	// line's formatted bytes, not the whole buffered backlog again.
	bufLenBefore := w.writeBuf.Len()

	if w.plain {
		// For plain serverless mode, just write the line content
		w.writeBuf.Write(lineContent)

		// Ensure line has a newline if it doesn't already
		if len(lineContent) > 0 && lineContent[len(lineContent)-1] != '\n' {
			w.writeBuf.WriteByte('\n')
		}
	} else {
		// For colored serverless mode with test compatibility
		// Build the complete line with protocol formatting for integration tests
		var lineBuf bytes.Buffer
		formatRemoteHeader(&lineBuf, w.hostname, defaultTransmittedPerc, lineNum, sourceID)

		// Remove trailing newline if present (it will be added back after coloring)
		content := lineContent
		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}
		lineBuf.Write(content)

		// Apply color formatting
		coloredLine := brush.Colorfy(lineBuf.String())
		w.writeBuf.WriteString(coloredLine)
		w.writeBuf.WriteByte('\n')
	}

	// Update stats: add only the delta appended for this line.
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len() - bufLenBefore)

	// Buffer writes for better performance - only flush when buffer is full
	if w.writeBuf.Len() >= w.bufSize {
		return w.flushBuffer()
	}

	return nil
}

// writeNetworkLine handles network mode output with protocol formatting.
// Adds protocol headers for non-plain mode. Must be called with mutex held.
func (w *DirectWriter) writeNetworkLine(lineContent []byte, lineNum uint64, sourceID string) error {
	// writeBuf accumulates lines until bufSize before flushing, so record its
	// length before appending: the bytesWritten stat must count only this
	// line's formatted bytes, not the whole buffered backlog again.
	bufLenBefore := w.writeBuf.Len()

	if w.plain {
		w.writeBuf.Write(lineContent)
		// In plain mode, ensure line has a newline if it doesn't already.
		if len(lineContent) > 0 && lineContent[len(lineContent)-1] != '\n' {
			w.writeBuf.WriteByte('\n')
		}
	} else {
		formatRemoteLine(&w.writeBuf, w.hostname, defaultTransmittedPerc, lineNum, sourceID, lineContent)
	}

	// Update stats: add only the delta appended for this line.
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len() - bufLenBefore)

	// Flush if buffer is getting full
	if w.writeBuf.Len() >= w.bufSize {
		return w.flushBuffer()
	}

	return nil
}

// WriteServerMessage writes a server message
func (w *DirectWriter) WriteServerMessage(message string) error {
	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		return nil
	}
	if w.serverless {
		return nil
	}

	w.mutex.Lock()
	defer w.mutex.Unlock()

	// Skip empty server messages when in plain mode
	if w.plain && (message == "" || message == "\n") {
		return nil
	}

	// Handle hidden messages
	if len(message) > 0 && message[0] == '.' {
		w.writeBuf.WriteString(message)
		w.writeBuf.WriteByte(protocol.MessageDelimiter)
		return w.flushBuffer()
	}

	formatServerMessage(&w.writeBuf, w.hostname, message, w.plain)

	return w.flushBuffer()
}

// Flush ensures all buffered data is written
func (w *DirectWriter) Flush() error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// Force flush any remaining data
	err := w.flushBuffer()

	// For serverless mode, ensure everything is written to output
	if w.serverless {
		// Ensure writer is flushed if it supports it
		if flusher, ok := w.writer.(interface{ Flush() error }); ok {
			err = errors.Join(err, flusher.Flush())
		}
	}

	return err
}

// flushBuffer writes the buffer content to the writer (must be called with mutex held)
func (w *DirectWriter) flushBuffer() error {
	if w.writeBuf.Len() == 0 {
		return nil
	}

	data := w.writeBuf.Bytes()

	// In serverless mode with colors, data is already processed line by line
	// so we don't need to do any additional formatting here

	for len(data) > 0 {
		n, err := w.writer.Write(data)
		if err != nil {
			w.writeBuf.Reset()
			return err
		}
		if n <= 0 {
			w.writeBuf.Reset()
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	w.writeBuf.Reset()

	return nil
}

// Stats returns writing statistics
func (w *DirectWriter) Stats() (linesWritten, bytesWritten uint64) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.linesWritten, w.bytesWritten
}

// ChannelWriter writes pre-formatted data to a output channel
type ChannelWriter struct {
	channel    chan<- []byte
	hostname   string
	plain      bool
	serverless bool
	generation uint64

	// Buffering for efficiency
	writeBuf bytes.Buffer
	bufSize  int
	mutex    sync.Mutex

	// Stats
	linesWritten uint64
	bytesWritten uint64

	activeGeneration func() uint64
}

var _ LineWriter = (*ChannelWriter)(nil)

// NewChannelWriter creates a writer that sends to a output channel
func NewChannelWriter(channel chan<- []byte, hostname string, plain, serverless bool) *ChannelWriter {
	return &ChannelWriter{
		channel:    channel,
		hostname:   hostname,
		plain:      plain,
		serverless: serverless,
		bufSize:    64 * 1024, // 64KB buffer
	}
}

// NewGeneratedChannelWriter creates a ChannelWriter bound to a session generation.
func NewGeneratedChannelWriter(channel chan<- []byte, hostname string, plain, serverless bool, generation uint64, activeGeneration func() uint64) *ChannelWriter {
	w := NewChannelWriter(channel, hostname, plain, serverless)
	w.generation = generation
	w.activeGeneration = activeGeneration
	return w
}

// WriteLineData formats and writes line data to the output channel
func (w *ChannelWriter) WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error {
	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		return nil
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// writeBuf is reset after every call here, so its length before appending
	// is always zero. Still use the explicit before/after delta so the stats
	// idiom stays uniform with the other output write paths.
	bufLenBefore := w.writeBuf.Len()

	if !w.plain && !w.serverless {
		formatRemoteLine(&w.writeBuf, w.hostname, defaultTransmittedPerc, lineNum, sourceID, lineContent)
	} else {
		w.writeBuf.Write(lineContent)
		w.writeBuf.WriteByte(protocol.MessageDelimiter)
	}

	// Update stats: add only the delta appended for this line.
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len() - bufLenBefore)

	// Send to channel
	data := make([]byte, w.writeBuf.Len())
	copy(data, w.writeBuf.Bytes())
	w.writeBuf.Reset()

	select {
	case w.channel <- encodeGeneratedBytes(w.generation, data):
		return nil
	default:
		return fmt.Errorf("output channel full")
	}
}

// WriteServerMessage writes a server message
func (w *ChannelWriter) WriteServerMessage(message string) error {
	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		return nil
	}
	if w.serverless {
		return nil
	}

	w.mutex.Lock()
	defer w.mutex.Unlock()

	// Skip empty server messages when in plain mode
	if w.plain && (message == "" || message == "\n") {
		return nil
	}

	var buf bytes.Buffer

	// Handle hidden messages
	if len(message) > 0 && message[0] == '.' {
		buf.WriteString(message)
		buf.WriteByte(protocol.MessageDelimiter)
	} else {
		formatServerMessage(&buf, w.hostname, message, w.plain)
	}

	data := buf.Bytes()
	select {
	case w.channel <- encodeGeneratedBytes(w.generation, data):
		return nil
	default:
		return fmt.Errorf("output channel full")
	}
}

// Flush is a no-op for channel writer as data is sent immediately
func (w *ChannelWriter) Flush() error {
	return nil
}

// Stats returns writing statistics
func (w *ChannelWriter) Stats() (linesWritten, bytesWritten uint64) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.linesWritten, w.bytesWritten
}

// NetworkWriter writes directly to the network connection bypassing channels
type NetworkWriter struct {
	outputLines     chan<- []byte
	serverMessages chan<- string
	hostname       string
	plain          bool
	serverless     bool
	generation     uint64
	ctx            context.Context

	// Internal buffer for batching writes
	writeBuf    bytes.Buffer
	bufSize     int
	mutex       sync.Mutex
	sendStateCh chan struct{}
	sending     bool

	// Stats
	linesWritten uint64
	bytesWritten uint64

	activeGeneration func() uint64
}

var _ LineWriter = (*NetworkWriter)(nil)

// NewNetworkWriter creates a NetworkWriter that batches formatted
// lines into a 64KB buffer before sending them to the output channel.
//
// bufSize is the field the previous bare struct literal in makeWriter
// omitted. With bufSize left at its zero value the flush condition in
// WriteLineData (writeBuf.Len() < w.bufSize) was never true off the
// backpressure path, so every line was sent as its own output-channel payload —
// one SSH packet and one write syscall per line. Setting bufSize to 64KB (the
// same value NewDirectWriter and NewChannelWriter use) lets many
// lines coalesce into a single send, which is the whole point of output output.
//
// Follow-mode (dtail tail) latency is preserved: tailWithProcessorOptimized
// calls processor.Flush() after every read chunk, and Flush drains the partial
// writeBuf to the output channel promptly, so batching never holds interactive
// output back past a read boundary.
func NewNetworkWriter(ctx context.Context, outputLines chan<- []byte,
	serverMessages chan<- string, hostname string, plain, serverless bool,
	generation uint64, activeGeneration func() uint64) *NetworkWriter {
	return &NetworkWriter{
		outputLines:       outputLines,
		serverMessages:   serverMessages,
		hostname:         hostname,
		plain:            plain,
		serverless:       serverless,
		generation:       generation,
		ctx:              ctx,
		bufSize:          64 * 1024, // 64KB buffer, matching the sibling output writers.
		sendStateCh:      make(chan struct{}),
		activeGeneration: activeGeneration,
	}
}

// WriteLineData formats and writes line data directly to the output channel.
// Builds the protocol-formatted line and sends it via sendToChannel.
func (w *NetworkWriter) WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error {
	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		return nil
	}
	w.mutex.Lock()

	// Per-line hot path (server mode): gate both traces so their uint64/int/string
	// args are not boxed on every line when trace is off. Evaluated once so the
	// second trace below shares the same decision.
	traceEnabled := dlog.Server.TraceEnabled()
	if traceEnabled {
		writerTrace("NetworkWriter.WriteLineData", "lineNum", lineNum, "sourceID", sourceID, "contentLen", len(lineContent))
	}

	// writeBuf accumulates lines until bufSize before flushing, so record its
	// length before appending: the bytesWritten stat must count only this
	// line's formatted bytes, not the whole buffered backlog again.
	bufLenBefore := w.writeBuf.Len()

	if !w.plain && !w.serverless {
		formatRemoteLine(&w.writeBuf, w.hostname, defaultTransmittedPerc, lineNum, sourceID, lineContent)
	} else {
		w.writeBuf.Write(lineContent)
		w.writeBuf.WriteByte(protocol.MessageDelimiter)
	}

	// Update stats: add only the delta appended for this line.
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len() - bufLenBefore)

	if traceEnabled {
		writerTrace("NetworkWriter.WriteLineData", "linesWritten", w.linesWritten, "bytesWritten", w.bytesWritten, "bufSize", w.writeBuf.Len())
	}

	if w.writeBuf.Len() < w.bufSize || w.sending {
		w.mutex.Unlock()
		return nil
	}

	data := append([]byte(nil), w.writeBuf.Bytes()...)
	w.writeBuf.Reset()
	w.markSendingLocked()
	w.mutex.Unlock()

	return w.sendBufferedData(data)
}

// sendBufferedData sends buffered data to the output channel while tracking the
// in-flight send state so Flush can wait for completion without holding the mutex.
func (w *NetworkWriter) sendBufferedData(data []byte) error {
	defer w.finishSending()
	return w.sendToChannel(data)
}

func (w *NetworkWriter) ensureSendStateChLocked() {
	if w.sendStateCh == nil {
		w.sendStateCh = make(chan struct{})
	}
}

func (w *NetworkWriter) markSendingLocked() {
	w.ensureSendStateChLocked()
	w.sending = true
}

func (w *NetworkWriter) finishSending() {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if !w.sending {
		return
	}

	oldCh := w.sendStateCh
	w.sendStateCh = make(chan struct{})
	w.sending = false

	if oldCh != nil {
		close(oldCh)
	}
}

func (w *NetworkWriter) waitForSendAvailability(ctx context.Context) error {
	for {
		w.mutex.Lock()
		if !w.sending {
			w.mutex.Unlock()
			return nil
		}

		stateCh := w.sendStateCh
		w.mutex.Unlock()

		select {
		case <-stateCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// sendToChannel sends buffered data to the output channel.
// It blocks only on the channel send itself and exits promptly when the
// request context is canceled.
func (w *NetworkWriter) sendToChannel(data []byte) error {
	// Per-send path (once per 64KB buffer flush): decide once so none of the
	// diagnostic traces below build a []interface{} or box their args when trace
	// is off. Cheaper and keeps the send loop tight.
	traceEnabled := dlog.Server.TraceEnabled()

	if w.outputLines == nil {
		if traceEnabled {
			writerTrace("NetworkWriter.sendToChannel", "outputLines channel is nil")
		}
		return nil
	}

	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		if traceEnabled {
			writerTrace("NetworkWriter.sendToChannel", "generation became stale before send")
		}
		return nil
	}

	encoded := encodeGeneratedBytes(w.generation, data)
	ctx := w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	retryDelay := defaultOutputReadRetryInterval

	if traceEnabled {
		writerTrace("NetworkWriter.sendToChannel", "sending to outputLines channel", "dataLen", len(data))
	}

	if err := ctx.Err(); err != nil {
		if traceEnabled {
			writerTrace("NetworkWriter.sendToChannel", "context already cancelled before send", "err", err)
		}
		return err
	}

	select {
	case w.outputLines <- encoded:
		if traceEnabled {
			writerTrace("NetworkWriter.sendToChannel", "sent to channel successfully")
		}
		return nil
	case <-ctx.Done():
		if traceEnabled {
			writerTrace("NetworkWriter.sendToChannel", "context cancelled while waiting to send")
		}
		return ctx.Err()
	default:
	}

	for {
		if !waitForGenerationRetry(ctx, w.generation, w.activeGeneration, retryDelay) {
			if err := ctx.Err(); err != nil {
				if traceEnabled {
					writerTrace("NetworkWriter.sendToChannel", "context cancelled while waiting to retry send", "err", err)
				}
				return err
			}
			if traceEnabled {
				writerTrace("NetworkWriter.sendToChannel", "generation became stale while waiting to retry send")
			}
			return nil
		}

		select {
		case w.outputLines <- encoded:
			if traceEnabled {
				writerTrace("NetworkWriter.sendToChannel", "sent to channel successfully")
			}
			return nil
		case <-ctx.Done():
			if traceEnabled {
				writerTrace("NetworkWriter.sendToChannel", "context cancelled while waiting to send")
			}
			return ctx.Err()
		default:
		}
	}
}

// WriteServerMessage writes a server message
func (w *NetworkWriter) WriteServerMessage(message string) error {
	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		return nil
	}
	// Server messages are less critical in output mode
	// We can send them through the normal channel
	if w.serverMessages != nil {
		select {
		case w.serverMessages <- encodeGeneratedMessage(w.generation, message):
			return nil
		default:
			return fmt.Errorf("server message channel full")
		}
	}
	return nil
}

// Flush ensures all data is written
func (w *NetworkWriter) Flush() error {
	writerTrace("NetworkWriter.Flush", "called")

	ctx := w.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		if err := w.waitForSendAvailability(ctx); err != nil {
			return err
		}

		w.mutex.Lock()
		if w.sending {
			w.mutex.Unlock()
			continue
		}
		if w.writeBuf.Len() == 0 {
			w.mutex.Unlock()
			break
		}

		writerTrace("NetworkWriter.Flush", "flushing buffered data", "bufSize", w.writeBuf.Len())

		data := append([]byte(nil), w.writeBuf.Bytes()...)
		w.writeBuf.Reset()
		w.markSendingLocked()
		w.mutex.Unlock()

		if err := w.sendBufferedData(data); err != nil {
			return err
		}
		writerTrace("NetworkWriter.Flush", "flushed data to channel")
	}

	writerTrace("NetworkWriter.Flush", "completed")

	return nil
}

// Stats returns writing statistics
func (w *NetworkWriter) Stats() (linesWritten, bytesWritten uint64) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.linesWritten, w.bytesWritten
}

// writerTrace forwards to dlog.Server.Trace for the cold/low-frequency
// output writer paths (Flush/Close, per-64KB sends). TraceEnabled() is nil-safe
// and also skips the work when trace is off. Per-line hot callers
// (DirectLineProcessor.ProcessLine, NetworkWriter.WriteLineData) must wrap
// their call in an explicit `if dlog.Server.TraceEnabled()` so the variadic
// slice and argument boxing are elided at the call site, not merely here.
func writerTrace(args ...interface{}) {
	if !dlog.Server.TraceEnabled() {
		return
	}
	dlog.Server.Trace(args...)
}

func shouldWriteGeneration(generation uint64, activeGeneration func() uint64) bool {
	if generation == 0 || activeGeneration == nil {
		return true
	}

	currentGeneration := activeGeneration()
	if currentGeneration == 0 {
		return true
	}

	return currentGeneration == generation
}

func waitForGenerationRetry(ctx context.Context, generation uint64, activeGeneration func() uint64, delay time.Duration) bool {
	if !shouldWriteGeneration(generation, activeGeneration) {
		return false
	}
	if delay <= 0 {
		return shouldWriteGeneration(generation, activeGeneration)
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}

	return shouldWriteGeneration(generation, activeGeneration)
}

// DirectLineProcessor processes lines directly without channels in output mode
type DirectLineProcessor struct {
	writer    LineWriter
	globID    string
	lineCount uint64
}

// NewDirectLineProcessor creates a processor that writes directly
func NewDirectLineProcessor(writer LineWriter, globID string) *DirectLineProcessor {
	return &DirectLineProcessor{
		writer: writer,
		globID: globID,
	}
}

// ProcessLine writes a line directly to the output
func (p *DirectLineProcessor) ProcessLine(lineContent *bytes.Buffer, lineNum uint64, sourceID string) error {
	p.lineCount++

	// Per-line hot path: gate the trace so the uint64/string args are not boxed
	// into a []interface{} on every line when trace logging is off (the default).
	// This call site was ~98% of all allocated objects and ~28% of CPU
	// (convT64+convTstring) in the output serverless dcat profile.
	if dlog.Server.TraceEnabled() {
		writerTrace("DirectLineProcessor.ProcessLine", "lineCount", p.lineCount, "lineNum", lineNum, "sourceID", sourceID)
	}

	// Write directly to output
	err := p.writer.WriteLineData(lineContent.Bytes(), lineNum, sourceID)

	// Recycle the buffer
	pool.RecycleBytesBuffer(lineContent)

	return err
}

// Flush ensures all data is written
func (p *DirectLineProcessor) Flush() error {
	writerTrace("DirectLineProcessor.Flush", "lineCount", p.lineCount)
	return p.writer.Flush()
}

// Close flushes any remaining data
func (p *DirectLineProcessor) Close() error {
	writerTrace("DirectLineProcessor.Close", "lineCount", p.lineCount)
	return p.writer.Flush()
}
