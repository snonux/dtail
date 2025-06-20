package handlers

import (
	"bytes"
	"fmt"
	"net"
	"os"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/user/server"
)

// NetworkOutputWriter provides direct network streaming
type NetworkOutputWriter struct {
	conn           net.Conn
	serverMessages chan<- string // Keep existing channel for server messages (low frequency)
	user           *server.User
	stats          interface{} // Keep interface compatible
}

// NewNetworkOutputWriter creates a new network output writer
func NewNetworkOutputWriter(conn net.Conn, serverMessages chan<- string, user *server.User) *NetworkOutputWriter {
	return &NetworkOutputWriter{
		conn:           conn,
		serverMessages: serverMessages,
		user:           user,
	}
}

// Write implements io.Writer interface for direct network streaming
func (now *NetworkOutputWriter) Write(data []byte) (int, error) {
	if now.conn == nil {
		// Fallback to stdout for serverless mode
		return os.Stdout.Write(data)
	}
	
	n, err := now.conn.Write(data)
	if err != nil {
		// Report network errors through existing server message channel
		now.sendServerMessage(fmt.Sprintf("Network write error: %v", err))
		return n, err
	}
	
	return n, nil
}

// sendServerMessage sends a message through the existing server message channel
func (now *NetworkOutputWriter) sendServerMessage(message string) {
	if now.serverMessages == nil {
		return
	}
	
	select {
	case now.serverMessages <- message:
		// Message sent successfully
	default:
		// Channel full, log the issue
		dlog.Server.Warn(now.user, "Server message channel full, dropping message:", message)
	}
}

// SendLine sends a formatted line directly to the network
func (now *NetworkOutputWriter) SendLine(hostname, filePath string, lineNum int, content []byte) error {
	// Format line using DTail protocol format: hostname|filepath|linenum|content\n
	formatted := make([]byte, 0, len(hostname)+len(filePath)+len(content)+50)
	formatted = append(formatted, hostname...)
	formatted = append(formatted, '|')
	formatted = append(formatted, filePath...)
	formatted = append(formatted, '|')
	
	// Add line number
	lineNumStr := fmt.Sprintf("%d", lineNum)
	formatted = append(formatted, lineNumStr...)
	formatted = append(formatted, '|')
	formatted = append(formatted, content...)
	formatted = append(formatted, '\n')
	
	_, err := now.Write(formatted)
	return err
}

// SendPlainLine sends a plain line without formatting
func (now *NetworkOutputWriter) SendPlainLine(content []byte) error {
	formatted := make([]byte, len(content)+1)
	copy(formatted, content)
	formatted[len(content)] = '\n'
	
	_, err := now.Write(formatted)
	return err
}

// SendServerStat sends a server statistics message
func (now *NetworkOutputWriter) SendServerStat(message string) {
	now.sendServerMessage(message)
}

// SendError sends an error message
func (now *NetworkOutputWriter) SendError(err error) {
	now.sendServerMessage(fmt.Sprintf("ERROR: %v", err))
}

// Close closes the network connection
func (now *NetworkOutputWriter) Close() error {
	if now.conn != nil {
		return now.conn.Close()
	}
	return nil
}

// BufferedNetworkWriter provides buffered writing for better network performance
type BufferedNetworkWriter struct {
	*NetworkOutputWriter
	buffer []byte
	size   int
}

// NewBufferedNetworkWriter creates a buffered network writer
func NewBufferedNetworkWriter(conn net.Conn, serverMessages chan<- string, user *server.User, bufferSize int) *BufferedNetworkWriter {
	return &BufferedNetworkWriter{
		NetworkOutputWriter: NewNetworkOutputWriter(conn, serverMessages, user),
		buffer:             make([]byte, 0, bufferSize),
		size:               bufferSize,
	}
}

// Write buffers writes for better network performance
func (bnw *BufferedNetworkWriter) Write(data []byte) (int, error) {
	totalWritten := 0
	
	for len(data) > 0 {
		// Check if we need to flush the buffer
		if len(bnw.buffer)+len(data) > bnw.size {
			// Flush current buffer
			if len(bnw.buffer) > 0 {
				if err := bnw.flush(); err != nil {
					return totalWritten, err
				}
			}
		}
		
		// If data is larger than buffer, write directly
		if len(data) > bnw.size {
			n, err := bnw.NetworkOutputWriter.Write(data)
			return totalWritten + n, err
		}
		
		// Add to buffer
		bnw.buffer = append(bnw.buffer, data...)
		totalWritten += len(data)
		break
	}
	
	return totalWritten, nil
}

// Flush writes buffered data to network
func (bnw *BufferedNetworkWriter) Flush() error {
	return bnw.flush()
}

func (bnw *BufferedNetworkWriter) flush() error {
	if len(bnw.buffer) == 0 {
		return nil
	}
	
	_, err := bnw.NetworkOutputWriter.Write(bnw.buffer)
	bnw.buffer = bnw.buffer[:0] // Reset buffer
	return err
}

// Close flushes and closes the connection
func (bnw *BufferedNetworkWriter) Close() error {
	if err := bnw.flush(); err != nil {
		return err
	}
	return bnw.NetworkOutputWriter.Close()
}

// ChannelOutputWriter sends output to the server's lines channel instead of direct network
type ChannelOutputWriter struct {
	linesCh        chan<- *line.Line
	serverMessages chan<- string
	user           *server.User
}

// NewChannelOutputWriter creates a new channel output writer
func NewChannelOutputWriter(linesCh chan<- *line.Line, serverMessages chan<- string, user *server.User) *ChannelOutputWriter {
	return &ChannelOutputWriter{
		linesCh:        linesCh,
		serverMessages: serverMessages,
		user:           user,
	}
}

// Write implements io.Writer interface by sending data through the lines channel
func (cow *ChannelOutputWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	
	// Create a line object using the proper constructor
	contentBuffer := bytes.NewBuffer(data)
	lineObj := line.New(contentBuffer, 0, 100, "direct")
	
	select {
	case cow.linesCh <- lineObj:
		return len(data), nil
	default:
		// Channel is full, report error
		cow.sendServerMessage("Lines channel full, dropping data")
		return 0, fmt.Errorf("lines channel full")
	}
}

// sendServerMessage sends a message through the existing server message channel
func (cow *ChannelOutputWriter) sendServerMessage(message string) {
	if cow.serverMessages == nil {
		return
	}
	
	select {
	case cow.serverMessages <- message:
		// Message sent successfully
	default:
		// Channel full, log the issue
		dlog.Server.Warn(cow.user, "Server message channel full, dropping message:", message)
	}
}

// SendLine sends a formatted line through the lines channel
func (cow *ChannelOutputWriter) SendLine(hostname, filePath string, lineNum int, content []byte) error {
	// Create a line object with proper metadata
	contentBuffer := bytes.NewBuffer(content)
	lineObj := line.New(contentBuffer, uint64(lineNum), 100, filePath)
	
	select {
	case cow.linesCh <- lineObj:
		return nil
	default:
		cow.sendServerMessage(fmt.Sprintf("Lines channel full, dropping line from %s:%d", filePath, lineNum))
		return fmt.Errorf("lines channel full")
	}
}

// SendPlainLine sends a plain line through the lines channel
func (cow *ChannelOutputWriter) SendPlainLine(content []byte) error {
	_, err := cow.Write(content)
	return err
}

// SendServerStat sends a server statistics message
func (cow *ChannelOutputWriter) SendServerStat(message string) {
	cow.sendServerMessage(message)
}

// SendError sends an error message
func (cow *ChannelOutputWriter) SendError(err error) {
	cow.sendServerMessage(fmt.Sprintf("ERROR: %v", err))
}

// Close is a no-op for channel output writer
func (cow *ChannelOutputWriter) Close() error {
	return nil
}

// ServerHandlerWriter writes output directly to the server handler's lines channel
type ServerHandlerWriter struct {
	server         *ServerHandler
	serverMessages chan<- string
	user           *server.User
}

// NewServerHandlerWriter creates a new server handler writer
func NewServerHandlerWriter(serverHandler *ServerHandler, serverMessages chan<- string, user *server.User) *ServerHandlerWriter {
	return &ServerHandlerWriter{
		server:         serverHandler,
		serverMessages: serverMessages,
		user:           user,
	}
}

// Write implements io.Writer interface by sending data through the server's lines channel
func (shw *ServerHandlerWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	
	// Create a line object and send it through the server's lines channel
	contentBuffer := bytes.NewBuffer(data)
	lineObj := line.New(contentBuffer, 0, 100, "direct")
	
	select {
	case shw.server.lines <- lineObj:
		return len(data), nil
	default:
		// Channel is full, report error
		shw.sendServerMessage("Server lines channel full, dropping data")
		return 0, fmt.Errorf("server lines channel full")
	}
}

// WriteLine implements LineWriter interface by sending data with proper sourceID
func (shw *ServerHandlerWriter) WriteLine(data []byte, sourceID string, stats interface{}) error {
	if len(data) == 0 {
		return nil
	}
	
	// Create a line object with proper sourceID
	contentBuffer := bytes.NewBuffer(data)
	
	// Extract stats if available
	var transmittedPerc int = 100
	var count uint64 = 0
	
	// Check if stats is fs.stats type (from internal/io/fs package)
	// For now, we'll use default values since the stats type is internal to fs package
	
	lineObj := line.New(contentBuffer, count, transmittedPerc, sourceID)
	
	select {
	case shw.server.lines <- lineObj:
		return nil
	default:
		// Channel is full, report error
		shw.sendServerMessage("Server lines channel full, dropping data")
		return fmt.Errorf("server lines channel full")
	}
}

// sendServerMessage sends a message through the existing server message channel
func (shw *ServerHandlerWriter) sendServerMessage(message string) {
	if shw.serverMessages == nil {
		return
	}
	
	select {
	case shw.serverMessages <- message:
		// Message sent successfully
	default:
		// Channel full, log the issue
		dlog.Server.Warn(shw.user, "Server message channel full, dropping message:", message)
	}
}

// SendLine sends a formatted line through the server's lines channel
func (shw *ServerHandlerWriter) SendLine(hostname, filePath string, lineNum int, content []byte) error {
	// Create a line object with proper metadata
	contentBuffer := bytes.NewBuffer(content)
	lineObj := line.New(contentBuffer, uint64(lineNum), 100, filePath)
	
	select {
	case shw.server.lines <- lineObj:
		return nil
	default:
		shw.sendServerMessage(fmt.Sprintf("Server lines channel full, dropping line from %s:%d", filePath, lineNum))
		return fmt.Errorf("server lines channel full")
	}
}

// SendPlainLine sends a plain line through the server's lines channel
func (shw *ServerHandlerWriter) SendPlainLine(content []byte) error {
	_, err := shw.Write(content)
	return err
}

// SendServerStat sends a server statistics message
func (shw *ServerHandlerWriter) SendServerStat(message string) {
	shw.sendServerMessage(message)
}

// SendError sends an error message
func (shw *ServerHandlerWriter) SendError(err error) {
	shw.sendServerMessage(fmt.Sprintf("ERROR: %v", err))
}

// Close is a no-op for server handler writer
func (shw *ServerHandlerWriter) Close() error {
	return nil
}