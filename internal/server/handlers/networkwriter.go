package handlers

import (
	"fmt"
	"net"
	"os"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/user/server"
)

// NetworkOutputWriter provides direct network streaming for channelless processing
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