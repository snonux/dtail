package handlers

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/protocol"
)

// GrepLineProcessor processes lines for grep operations without using channels.
// It writes directly to the output writer for better performance.
type GrepLineProcessor struct {
	writer     io.Writer
	hostname   string
	plain      bool
	serverless bool
	
	// Buffering for efficiency
	writeBuf   bytes.Buffer
	bufSize    int
	mutex      sync.Mutex
	
	// Stats
	linesProcessed uint64
	bytesWritten   uint64
}

var _ line.Processor = (*GrepLineProcessor)(nil)

// HandlerWriter adapts a ServerHandler to implement io.Writer
type HandlerWriter struct {
	handler        *ServerHandler
	serverMessages chan<- string
}

// Write sends data through the server messages channel
func (w *HandlerWriter) Write(p []byte) (n int, err error) {
	// Convert bytes to string and send through serverMessages channel
	// This will be picked up by the handler's Read() method
	message := string(p)
	select {
	case w.serverMessages <- message:
		return len(p), nil
	default:
		return 0, fmt.Errorf("server messages channel full")
	}
}

// NewGrepLineProcessor creates a new processor for grep operations.
func NewGrepLineProcessor(writer io.Writer, hostname string, plain, serverless bool) *GrepLineProcessor {
	return &GrepLineProcessor{
		writer:     writer,
		hostname:   hostname,
		plain:      plain,
		serverless: serverless,
		bufSize:    64 * 1024, // 64KB buffer
	}
}

// ProcessLine processes a single line and writes it to the output.
func (p *GrepLineProcessor) ProcessLine(lineContent *bytes.Buffer, lineNum uint64, sourceID string) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	
	// Build the output line
	if !p.plain && !p.serverless {
		p.writeBuf.WriteString("REMOTE")
		p.writeBuf.WriteString(protocol.FieldDelimiter)
		p.writeBuf.WriteString(p.hostname)
		p.writeBuf.WriteString(protocol.FieldDelimiter)
		// For grep, we don't have transmittedPerc, so use 100
		p.writeBuf.WriteString("100")
		p.writeBuf.WriteString(protocol.FieldDelimiter)
		p.writeBuf.WriteString(fmt.Sprintf("%v", lineNum))
		p.writeBuf.WriteString(protocol.FieldDelimiter)
		p.writeBuf.WriteString(sourceID)
		p.writeBuf.WriteString(protocol.FieldDelimiter)
	}
	
	// Write the actual line content
	p.writeBuf.Write(lineContent.Bytes())
	p.writeBuf.WriteByte(protocol.MessageDelimiter)
	
	// Recycle the line buffer
	pool.RecycleBytesBuffer(lineContent)
	
	// Update stats
	p.linesProcessed++
	p.bytesWritten += uint64(p.writeBuf.Len())
	
	// Flush if buffer is getting full
	if p.writeBuf.Len() >= p.bufSize {
		return p.flushBuffer()
	}
	
	return nil
}

// Flush writes any buffered data to the output.
func (p *GrepLineProcessor) Flush() error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	
	return p.flushBuffer()
}

// flushBuffer writes the buffer content to the writer (must be called with mutex held).
func (p *GrepLineProcessor) flushBuffer() error {
	if p.writeBuf.Len() == 0 {
		return nil
	}
	
	_, err := p.writer.Write(p.writeBuf.Bytes())
	p.writeBuf.Reset()
	
	return err
}

// Close cleans up the processor.
func (p *GrepLineProcessor) Close() error {
	// Flush any remaining data
	return p.Flush()
}

// Stats returns processing statistics.
func (p *GrepLineProcessor) Stats() (linesProcessed, bytesWritten uint64) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	
	return p.linesProcessed, p.bytesWritten
}

// ServerMessageProcessor handles server messages separately from line data.
type ServerMessageProcessor struct {
	writer     io.Writer
	hostname   string
	plain      bool
	serverless bool
	mutex      sync.Mutex
}

// NewServerMessageProcessor creates a processor for server messages.
func NewServerMessageProcessor(writer io.Writer, hostname string, plain, serverless bool) *ServerMessageProcessor {
	return &ServerMessageProcessor{
		writer:     writer,
		hostname:   hostname,
		plain:      plain,
		serverless: serverless,
	}
}

// SendMessage sends a server message.
func (p *ServerMessageProcessor) SendMessage(message string) error {
	if p.serverless {
		return nil
	}
	
	p.mutex.Lock()
	defer p.mutex.Unlock()
	
	var buf bytes.Buffer
	
	// Skip empty server messages when in plain mode
	if p.plain && (message == "" || message == "\n") {
		return nil
	}
	
	// Handle hidden messages
	if len(message) > 0 && message[0] == '.' {
		buf.WriteString(message)
		buf.WriteByte(protocol.MessageDelimiter)
		_, err := p.writer.Write(buf.Bytes())
		return err
	}
	
	// Handle normal server message
	if !p.plain {
		buf.WriteString("SERVER")
		buf.WriteString(protocol.FieldDelimiter)
		buf.WriteString(p.hostname)
		buf.WriteString(protocol.FieldDelimiter)
	}
	buf.WriteString(message)
	buf.WriteByte(protocol.MessageDelimiter)
	
	_, err := p.writer.Write(buf.Bytes())
	return err
}