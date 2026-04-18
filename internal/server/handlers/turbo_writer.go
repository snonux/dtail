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

// TurboWriter defines the interface for direct writing in turbo mode
type TurboWriter interface {
	// WriteLineData writes formatted line data directly to output
	WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error
	// WriteServerMessage writes a server message
	WriteServerMessage(message string) error
	// Flush ensures all buffered data is written
	Flush() error
}

// DirectTurboWriter implements TurboWriter for direct network writing
type DirectTurboWriter struct {
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

var _ TurboWriter = (*DirectTurboWriter)(nil)

// NewDirectTurboWriter creates a new turbo writer
func NewDirectTurboWriter(writer io.Writer, hostname string, plain, serverless bool) *DirectTurboWriter {
	return &DirectTurboWriter{
		writer:     writer,
		hostname:   hostname,
		plain:      plain,
		serverless: serverless,
		bufSize:    64 * 1024, // 64KB buffer
	}
}

// NewGeneratedDirectTurboWriter creates a DirectTurboWriter bound to a session generation.
func NewGeneratedDirectTurboWriter(writer io.Writer, hostname string, plain, serverless bool, generation uint64, activeGeneration func() uint64) *DirectTurboWriter {
	w := NewDirectTurboWriter(writer, hostname, plain, serverless)
	w.generation = generation
	w.activeGeneration = activeGeneration
	return w
}

// WriteLineData writes formatted line data directly to output.
// Dispatches to serverless or network mode handlers based on configuration.
func (w *DirectTurboWriter) WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error {
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
func (w *DirectTurboWriter) writeServerlessLine(lineContent []byte, lineNum uint64, sourceID string) error {
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

	// Update stats
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len())

	// Buffer writes for better performance - only flush when buffer is full
	if w.writeBuf.Len() >= w.bufSize {
		return w.flushBuffer()
	}

	return nil
}

// writeNetworkLine handles network mode output with protocol formatting.
// Adds protocol headers for non-plain mode. Must be called with mutex held.
func (w *DirectTurboWriter) writeNetworkLine(lineContent []byte, lineNum uint64, sourceID string) error {
	if w.plain {
		w.writeBuf.Write(lineContent)
		// In plain mode, ensure line has a newline if it doesn't already.
		if len(lineContent) > 0 && lineContent[len(lineContent)-1] != '\n' {
			w.writeBuf.WriteByte('\n')
		}
	} else {
		formatRemoteLine(&w.writeBuf, w.hostname, defaultTransmittedPerc, lineNum, sourceID, lineContent)
	}

	// Update stats
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len())

	// Flush if buffer is getting full
	if w.writeBuf.Len() >= w.bufSize {
		return w.flushBuffer()
	}

	return nil
}

// WriteServerMessage writes a server message
func (w *DirectTurboWriter) WriteServerMessage(message string) error {
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
func (w *DirectTurboWriter) Flush() error {
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
func (w *DirectTurboWriter) flushBuffer() error {
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
func (w *DirectTurboWriter) Stats() (linesWritten, bytesWritten uint64) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.linesWritten, w.bytesWritten
}

// TurboChannelWriter writes pre-formatted data to a turbo channel
type TurboChannelWriter struct {
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

var _ TurboWriter = (*TurboChannelWriter)(nil)

// NewTurboChannelWriter creates a writer that sends to a turbo channel
func NewTurboChannelWriter(channel chan<- []byte, hostname string, plain, serverless bool) *TurboChannelWriter {
	return &TurboChannelWriter{
		channel:    channel,
		hostname:   hostname,
		plain:      plain,
		serverless: serverless,
		bufSize:    64 * 1024, // 64KB buffer
	}
}

// NewGeneratedTurboChannelWriter creates a TurboChannelWriter bound to a session generation.
func NewGeneratedTurboChannelWriter(channel chan<- []byte, hostname string, plain, serverless bool, generation uint64, activeGeneration func() uint64) *TurboChannelWriter {
	w := NewTurboChannelWriter(channel, hostname, plain, serverless)
	w.generation = generation
	w.activeGeneration = activeGeneration
	return w
}

// WriteLineData formats and writes line data to the turbo channel
func (w *TurboChannelWriter) WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error {
	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		return nil
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if !w.plain && !w.serverless {
		formatRemoteLine(&w.writeBuf, w.hostname, defaultTransmittedPerc, lineNum, sourceID, lineContent)
	} else {
		w.writeBuf.Write(lineContent)
		w.writeBuf.WriteByte(protocol.MessageDelimiter)
	}

	// Update stats
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len())

	// Send to channel
	data := make([]byte, w.writeBuf.Len())
	copy(data, w.writeBuf.Bytes())
	w.writeBuf.Reset()

	select {
	case w.channel <- encodeGeneratedBytes(w.generation, data):
		return nil
	default:
		return fmt.Errorf("turbo channel full")
	}
}

// WriteServerMessage writes a server message
func (w *TurboChannelWriter) WriteServerMessage(message string) error {
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
		return fmt.Errorf("turbo channel full")
	}
}

// Flush is a no-op for channel writer as data is sent immediately
func (w *TurboChannelWriter) Flush() error {
	return nil
}

// Stats returns writing statistics
func (w *TurboChannelWriter) Stats() (linesWritten, bytesWritten uint64) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.linesWritten, w.bytesWritten
}

// TurboNetworkWriter writes directly to the network connection bypassing channels
type TurboNetworkWriter struct {
	turboLines     chan<- []byte
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

// WriteLineData formats and writes line data directly to the turbo channel.
// Builds the protocol-formatted line and sends it via sendToTurboChannel.
func (w *TurboNetworkWriter) WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error {
	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		return nil
	}
	w.mutex.Lock()

	turboWriterTrace("TurboNetworkWriter.WriteLineData", "lineNum", lineNum, "sourceID", sourceID, "contentLen", len(lineContent))

	if !w.plain && !w.serverless {
		formatRemoteLine(&w.writeBuf, w.hostname, defaultTransmittedPerc, lineNum, sourceID, lineContent)
	} else {
		w.writeBuf.Write(lineContent)
		w.writeBuf.WriteByte(protocol.MessageDelimiter)
	}

	// Update stats
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len())

	turboWriterTrace("TurboNetworkWriter.WriteLineData", "linesWritten", w.linesWritten, "bytesWritten", w.bytesWritten, "bufSize", w.writeBuf.Len())

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

// sendBufferedData sends buffered data to the turbo channel while tracking the
// in-flight send state so Flush can wait for completion without holding the mutex.
func (w *TurboNetworkWriter) sendBufferedData(data []byte) error {
	defer w.finishSending()
	return w.sendToTurboChannel(data)
}

func (w *TurboNetworkWriter) ensureSendStateChLocked() {
	if w.sendStateCh == nil {
		w.sendStateCh = make(chan struct{})
	}
}

func (w *TurboNetworkWriter) markSendingLocked() {
	w.ensureSendStateChLocked()
	w.sending = true
}

func (w *TurboNetworkWriter) finishSending() {
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

func (w *TurboNetworkWriter) waitForSendAvailability(ctx context.Context) error {
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

// sendToTurboChannel sends buffered data to the turbo channel.
// It blocks only on the channel send itself and exits promptly when the
// request context is canceled.
func (w *TurboNetworkWriter) sendToTurboChannel(data []byte) error {
	if w.turboLines == nil {
		turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "turboLines channel is nil")
		return nil
	}

	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "generation became stale before send")
		return nil
	}

	encoded := encodeGeneratedBytes(w.generation, data)
	ctx := w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	retryDelay := defaultTurboReadRetryInterval

	turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "sending to turboLines channel", "dataLen", len(data))

	if err := ctx.Err(); err != nil {
		turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "context already cancelled before send", "err", err)
		return err
	}

	select {
	case w.turboLines <- encoded:
		turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "sent to channel successfully")
		return nil
	case <-ctx.Done():
		turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "context cancelled while waiting to send")
		return ctx.Err()
	default:
	}

	for {
		if !waitForGenerationRetry(ctx, w.generation, w.activeGeneration, retryDelay) {
			if err := ctx.Err(); err != nil {
				turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "context cancelled while waiting to retry send", "err", err)
				return err
			}
			turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "generation became stale while waiting to retry send")
			return nil
		}

		select {
		case w.turboLines <- encoded:
			turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "sent to channel successfully")
			return nil
		case <-ctx.Done():
			turboWriterTrace("TurboNetworkWriter.sendToTurboChannel", "context cancelled while waiting to send")
			return ctx.Err()
		default:
		}
	}
}

// WriteServerMessage writes a server message
func (w *TurboNetworkWriter) WriteServerMessage(message string) error {
	if !shouldWriteGeneration(w.generation, w.activeGeneration) {
		return nil
	}
	// Server messages are less critical in turbo mode
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
func (w *TurboNetworkWriter) Flush() error {
	turboWriterTrace("TurboNetworkWriter.Flush", "called")

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

		turboWriterTrace("TurboNetworkWriter.Flush", "flushing buffered data", "bufSize", w.writeBuf.Len())

		data := append([]byte(nil), w.writeBuf.Bytes()...)
		w.writeBuf.Reset()
		w.markSendingLocked()
		w.mutex.Unlock()

		if err := w.sendBufferedData(data); err != nil {
			return err
		}
		turboWriterTrace("TurboNetworkWriter.Flush", "flushed data to channel")
	}

	turboWriterTrace("TurboNetworkWriter.Flush", "completed")

	return nil
}

func turboWriterTrace(args ...interface{}) {
	if dlog.Server == nil {
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

// DirectLineProcessor processes lines directly without channels in turbo mode
type DirectLineProcessor struct {
	writer    TurboWriter
	globID    string
	lineCount uint64
}

// NewDirectLineProcessor creates a processor that writes directly
func NewDirectLineProcessor(writer TurboWriter, globID string) *DirectLineProcessor {
	return &DirectLineProcessor{
		writer: writer,
		globID: globID,
	}
}

// ProcessLine writes a line directly to the output
func (p *DirectLineProcessor) ProcessLine(lineContent *bytes.Buffer, lineNum uint64, sourceID string) error {
	p.lineCount++

	turboWriterTrace("DirectLineProcessor.ProcessLine", "lineCount", p.lineCount, "lineNum", lineNum, "sourceID", sourceID)

	// Write directly to output
	err := p.writer.WriteLineData(lineContent.Bytes(), lineNum, sourceID)

	// Recycle the buffer
	pool.RecycleBytesBuffer(lineContent)

	return err
}

// Flush ensures all data is written
func (p *DirectLineProcessor) Flush() error {
	turboWriterTrace("DirectLineProcessor.Flush", "lineCount", p.lineCount)
	return p.writer.Flush()
}

// Close flushes any remaining data
func (p *DirectLineProcessor) Close() error {
	turboWriterTrace("DirectLineProcessor.Close", "lineCount", p.lineCount)
	return p.writer.Flush()
}
