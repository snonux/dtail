package handlers

import (
	"bytes"
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

	// Buffering for efficiency
	writeBuf bytes.Buffer
	bufSize  int
	mutex    sync.Mutex

	// Stats
	linesWritten uint64
	bytesWritten uint64
}

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

// WriteLineData writes formatted line data directly to output
func (w *DirectTurboWriter) WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// In serverless mode with colors, write each line immediately
	if w.serverless && !w.plain {
		// Build the complete line in a temporary buffer
		var lineBuf bytes.Buffer
		lineBuf.WriteString("REMOTE")
		lineBuf.WriteString(protocol.FieldDelimiter)
		lineBuf.WriteString(w.hostname)
		lineBuf.WriteString(protocol.FieldDelimiter)
		lineBuf.WriteString("100")
		lineBuf.WriteString(protocol.FieldDelimiter)
		lineBuf.WriteString(fmt.Sprintf("%v", lineNum))
		lineBuf.WriteString(protocol.FieldDelimiter)
		lineBuf.WriteString(sourceID)
		lineBuf.WriteString(protocol.FieldDelimiter)
		
		// Remove trailing newline if present (it will be added back after coloring)
		content := lineContent
		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}
		lineBuf.Write(content)
		
		// Apply color formatting
		coloredLine := brush.Colorfy(lineBuf.String())
		
		// Write directly to output with newline
		_, err := w.writer.Write([]byte(coloredLine + "\n"))
		w.linesWritten++
		w.bytesWritten += uint64(len(coloredLine) + 1)
		return err
	}

	// Build the output line
	if !w.plain {
		w.writeBuf.WriteString("REMOTE")
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(w.hostname)
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		// For direct writing, we don't have transmittedPerc, so use 100
		w.writeBuf.WriteString("100")
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(fmt.Sprintf("%v", lineNum))
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(sourceID)
		w.writeBuf.WriteString(protocol.FieldDelimiter)
	}

	// Write the actual line content
	w.writeBuf.Write(lineContent)

	// In plain mode, ensure line has a newline if it doesn't already
	if w.plain && len(lineContent) > 0 && lineContent[len(lineContent)-1] != '\n' {
		w.writeBuf.WriteByte('\n')
	}

	// Only add message delimiter in non-plain, non-serverless mode
	// In serverless mode, we output lines directly
	if !w.plain && !w.serverless {
		w.writeBuf.WriteByte(protocol.MessageDelimiter)
	}

	// Update stats
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len())

	// Flush if buffer is getting full or in serverless mode
	if w.writeBuf.Len() >= w.bufSize || w.serverless {
		return w.flushBuffer()
	}

	return nil
}

// WriteServerMessage writes a server message
func (w *DirectTurboWriter) WriteServerMessage(message string) error {
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

	// Handle normal server message
	if !w.plain {
		w.writeBuf.WriteString("SERVER")
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(w.hostname)
		w.writeBuf.WriteString(protocol.FieldDelimiter)
	}
	w.writeBuf.WriteString(message)
	w.writeBuf.WriteByte(protocol.MessageDelimiter)

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
			flusher.Flush()
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

	_, err := w.writer.Write(data)
	w.writeBuf.Reset()

	return err
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

	// Buffering for efficiency
	writeBuf bytes.Buffer
	bufSize  int
	mutex    sync.Mutex

	// Stats
	linesWritten uint64
	bytesWritten uint64
}

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

// WriteLineData formats and writes line data to the turbo channel
func (w *TurboChannelWriter) WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// Build the output line
	if !w.plain && !w.serverless {
		w.writeBuf.WriteString("REMOTE")
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(w.hostname)
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		// For direct writing, we don't have transmittedPerc, so use 100
		w.writeBuf.WriteString("100")
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(fmt.Sprintf("%v", lineNum))
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(sourceID)
		w.writeBuf.WriteString(protocol.FieldDelimiter)
	}

	// Write the actual line content (already includes line endings)
	w.writeBuf.Write(lineContent)
	w.writeBuf.WriteByte(protocol.MessageDelimiter)

	// Update stats
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len())

	// Send to channel
	data := make([]byte, w.writeBuf.Len())
	copy(data, w.writeBuf.Bytes())
	w.writeBuf.Reset()

	select {
	case w.channel <- data:
		return nil
	default:
		return fmt.Errorf("turbo channel full")
	}
}

// WriteServerMessage writes a server message
func (w *TurboChannelWriter) WriteServerMessage(message string) error {
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
		// Handle normal server message
		if !w.plain {
			buf.WriteString("SERVER")
			buf.WriteString(protocol.FieldDelimiter)
			buf.WriteString(w.hostname)
			buf.WriteString(protocol.FieldDelimiter)
		}
		buf.WriteString(message)
		buf.WriteByte(protocol.MessageDelimiter)
	}

	data := buf.Bytes()
	select {
	case w.channel <- data:
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
	handler    *baseHandler
	hostname   string
	plain      bool
	serverless bool

	// Internal buffer for batching writes
	writeBuf bytes.Buffer
	bufSize  int
	mutex    sync.Mutex

	// Direct output channel for turbo mode
	outputChan chan []byte

	// Stats
	linesWritten uint64
	bytesWritten uint64
}

// WriteLineData formats and writes line data directly
func (w *TurboNetworkWriter) WriteLineData(lineContent []byte, lineNum uint64, sourceID string) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	dlog.Server.Trace("TurboNetworkWriter.WriteLineData", "lineNum", lineNum, "sourceID", sourceID, "contentLen", len(lineContent))

	// Build the output line
	if !w.plain && !w.serverless {
		w.writeBuf.WriteString("REMOTE")
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(w.hostname)
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		// For direct writing, we don't have transmittedPerc, so use 100
		w.writeBuf.WriteString("100")
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(fmt.Sprintf("%v", lineNum))
		w.writeBuf.WriteString(protocol.FieldDelimiter)
		w.writeBuf.WriteString(sourceID)
		w.writeBuf.WriteString(protocol.FieldDelimiter)
	}

	// Write the actual line content (already includes line endings)
	w.writeBuf.Write(lineContent)
	w.writeBuf.WriteByte(protocol.MessageDelimiter)

	// Update stats
	w.linesWritten++
	w.bytesWritten += uint64(w.writeBuf.Len())

	dlog.Server.Trace("TurboNetworkWriter.WriteLineData", "linesWritten", w.linesWritten, "bytesWritten", w.bytesWritten, "bufSize", w.writeBuf.Len())

	// Write directly to the turbo channel
	if w.handler.turboLines != nil {
		data := make([]byte, w.writeBuf.Len())
		copy(data, w.writeBuf.Bytes())

		dlog.Server.Trace("TurboNetworkWriter.WriteLineData", "sending to turboLines channel", "dataLen", len(data))

		// Send data to turbo channel with a larger buffer
		select {
		case w.handler.turboLines <- data:
			dlog.Server.Trace("TurboNetworkWriter.WriteLineData", "sent to channel successfully")
			w.writeBuf.Reset()
			return nil
		default:
			// Channel full, wait a bit and retry
			dlog.Server.Trace("TurboNetworkWriter.WriteLineData", "channel full, waiting before retry")
			time.Sleep(time.Millisecond)
			w.handler.turboLines <- data
			dlog.Server.Trace("TurboNetworkWriter.WriteLineData", "sent to channel after retry")
			w.writeBuf.Reset()
			return nil
		}
	}

	dlog.Server.Trace("TurboNetworkWriter.WriteLineData", "turboLines channel is nil")
	w.writeBuf.Reset()
	return nil
}

// WriteServerMessage writes a server message
func (w *TurboNetworkWriter) WriteServerMessage(message string) error {
	// Server messages are less critical in turbo mode
	// We can send them through the normal channel
	if w.handler != nil && w.handler.serverMessages != nil {
		select {
		case w.handler.serverMessages <- message:
			return nil
		default:
			return fmt.Errorf("server message channel full")
		}
	}
	return nil
}

// Flush ensures all data is written
func (w *TurboNetworkWriter) Flush() error {
	dlog.Server.Trace("TurboNetworkWriter.Flush", "called")
	
	w.mutex.Lock()
	defer w.mutex.Unlock()
	
	// If we have any buffered data, send it now
	if w.writeBuf.Len() > 0 {
		dlog.Server.Trace("TurboNetworkWriter.Flush", "flushing buffered data", "bufSize", w.writeBuf.Len())
		
		if w.handler.turboLines != nil {
			data := make([]byte, w.writeBuf.Len())
			copy(data, w.writeBuf.Bytes())
			
			// Force send the data
			w.handler.turboLines <- data
			w.writeBuf.Reset()
			dlog.Server.Trace("TurboNetworkWriter.Flush", "flushed data to channel")
		}
	}
	
	// Wait for the channel to have some space to ensure data is being processed
	// Don't close the EOF channel here as it may be used for multiple files
	if w.handler.turboLines != nil {
		// Wait until channel has been drained somewhat
		for i := 0; i < 100 && len(w.handler.turboLines) > 900; i++ {
			dlog.Server.Trace("TurboNetworkWriter.Flush", "waiting for channel to drain", "channelLen", len(w.handler.turboLines))
			time.Sleep(10 * time.Millisecond)
		}
		dlog.Server.Trace("TurboNetworkWriter.Flush", "channel status", "channelLen", len(w.handler.turboLines))
	}
	
	// Wait a bit to ensure data is processed
	// This is crucial for integration tests
	time.Sleep(10 * time.Millisecond)
	dlog.Server.Trace("TurboNetworkWriter.Flush", "completed")
	
	return nil
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

	dlog.Server.Trace("DirectLineProcessor.ProcessLine", "lineCount", p.lineCount, "lineNum", lineNum, "sourceID", sourceID)

	// Write directly to output
	err := p.writer.WriteLineData(lineContent.Bytes(), lineNum, sourceID)

	// Recycle the buffer
	pool.RecycleBytesBuffer(lineContent)

	return err
}

// Flush ensures all data is written
func (p *DirectLineProcessor) Flush() error {
	dlog.Server.Trace("DirectLineProcessor.Flush", "lineCount", p.lineCount)
	return p.writer.Flush()
}

// Close flushes any remaining data
func (p *DirectLineProcessor) Close() error {
	dlog.Server.Trace("DirectLineProcessor.Close", "lineCount", p.lineCount)
	return p.writer.Flush()
}
