package testutil

import (
	"fmt"
	"io"
	"net"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// MockSSHServer provides a mock SSH server for testing.
type MockSSHServer struct {
	t          *testing.T
	listener   net.Listener
	config     *ssh.ServerConfig
	handlers   map[string]ChannelHandler
	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
	connections []ssh.Conn
}

// ChannelHandler handles a specific channel type.
type ChannelHandler func(channel ssh.Channel, requests <-chan *ssh.Request)

// NewMockSSHServer creates a new mock SSH server.
func NewMockSSHServer(t *testing.T) *MockSSHServer {
	privateKey := generateTestPrivateKey(t)
	
	config := &ssh.ServerConfig{
		NoClientAuth: true,
	}
	config.AddHostKey(privateKey)
	
	return &MockSSHServer{
		t:        t,
		config:   config,
		handlers: make(map[string]ChannelHandler),
		stopCh:   make(chan struct{}),
	}
}

// AddHandler adds a channel handler for a specific channel type.
func (s *MockSSHServer) AddHandler(channelType string, handler ChannelHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[channelType] = handler
}

// Start starts the mock SSH server.
func (s *MockSSHServer) Start() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	
	s.listener = listener
	s.running = true
	
	go s.acceptConnections()
	
	return listener.Addr().String(), nil
}

// Stop stops the mock SSH server.
func (s *MockSSHServer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	if !s.running {
		return
	}
	
	s.running = false
	close(s.stopCh)
	
	if s.listener != nil {
		s.listener.Close()
	}
	
	for _, conn := range s.connections {
		conn.Close()
	}
}

func (s *MockSSHServer) acceptConnections() {
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		
		conn, err := s.listener.Accept()
		if err != nil {
			if !s.running {
				return
			}
			s.t.Logf("error accepting connection: %v", err)
			continue
		}
		
		go s.handleConnection(conn)
	}
}

func (s *MockSSHServer) handleConnection(netConn net.Conn) {
	sshConn, chans, reqs, err := ssh.NewServerConn(netConn, s.config)
	if err != nil {
		s.t.Logf("error creating SSH connection: %v", err)
		netConn.Close()
		return
	}
	
	s.mu.Lock()
	s.connections = append(s.connections, sshConn)
	s.mu.Unlock()
	
	go ssh.DiscardRequests(reqs)
	
	for newChannel := range chans {
		s.mu.Lock()
		handler, ok := s.handlers[newChannel.ChannelType()]
		s.mu.Unlock()
		
		if !ok {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		
		channel, requests, err := newChannel.Accept()
		if err != nil {
			s.t.Logf("error accepting channel: %v", err)
			continue
		}
		
		go handler(channel, requests)
	}
}

// generateTestPrivateKey generates a test RSA private key.
func generateTestPrivateKey(t *testing.T) ssh.Signer {
	// This is a test key - DO NOT use in production!
	testKey := []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAw7IN7mpC1jvM6QwFAiEAQF3C+nzFmXH8LoWiPPQdqTY1Wnxl
G7Bfq2lAIqbLxQKCAQBVu3buJKZKZH7KvdxVDHQAzPxYLc11qdplDIwHnWH3VRyw
U0HcY9KwGLDSIa3H4oGAWGHQvB4lsQ4JQNZ4h5PuWPGH8laGvT6NGsJCCUJ3vN9P
OI1/2jnB9N5Jvx0j5c7EbDAJgDckKGUBGpL9TJxDXhY0c1cP9Pds30BfFhq9Z7Gx
v8JIw+IXQJ/mVpGXNKjVAqGQelMBRLUbQP/5N8J8CQXM+EcRcgc9WNiD9sF3LLQZ
6hnoJOpMhXIHHqA6l8tlX4Lzd5NAYLDpNH8JbJ6FoGW3EhzLd7mHg0YPDc3F5Aqp
MIIBAgMBAAECggEALQ3pT5NQ6VPLbxNAJljnKRXBbCMmQ3b7kZe1en2H8s1v3R6F
hGAzc4IodNbBYNMNLDp4xvvYCHANmYJhaSqHUtFdkE3UFfCJZQ4vL/fKGWLKAcNH
PXNr1V0zNGYPOgJ3keVz2xtB6KLJmIqP8LoQW8NqG5nQXhQE8svVQq3melPNVLNP
TiBRRStGPTJekV8HBMn6NQKBgQDjJZQKzGjJ/XR7ko3Tp9dQVQKKwLY5UgKgL8rL
3hvVZFdOJ1wkUPHCKFl8m6PKJpB9yMB0wmV4OPnJ4KE0QTLa7wTyzQKBgQDbPTql
yD8JGT3Vn3Yjv1mT3Kw5H8Y6OQ/pF8qGQB8JKCn1vJ8S3u0OQGg8cF8Y6LQT3wQr
e2JBQmqYJ3Yl1kP8xQKBgFLw3HQLqT9J4wJPuPQKBgGl8nQKBgQC2J2vJ8wPQVLnQ
8wQXMQvJ0wCm1v7X5l3t4lH8LQH3jQKBgFQXJYlNjGNQ3rJ8
-----END RSA PRIVATE KEY-----`)
	
	signer, err := ssh.ParsePrivateKey(testKey)
	if err != nil {
		t.Fatalf("failed to parse test private key: %v", err)
	}
	
	return signer
}

// DefaultSessionHandler provides a default session handler that executes commands.
func DefaultSessionHandler() ChannelHandler {
	return func(channel ssh.Channel, requests <-chan *ssh.Request) {
		defer channel.Close()
		
		for req := range requests {
			switch req.Type {
			case "exec":
				// Simple echo implementation for testing
				cmd := string(req.Payload[4:]) // Skip the length prefix
				response := fmt.Sprintf("mock response for: %s\n", cmd)
				channel.Write([]byte(response))
				
				if req.WantReply {
					req.Reply(true, nil)
				}
				
				// Send exit status
				exitStatus := []byte{0, 0, 0, 0} // Success
				channel.SendRequest("exit-status", false, exitStatus)
				return
				
			case "shell":
				if req.WantReply {
					req.Reply(true, nil)
				}
				
				// Simple shell that echoes input
				go func() {
					buf := make([]byte, 1024)
					for {
						n, err := channel.Read(buf)
						if err != nil {
							return
						}
						channel.Write(buf[:n])
					}
				}()
				
			default:
				if req.WantReply {
					req.Reply(false, nil)
				}
			}
		}
	}
}

// EchoHandler returns a handler that echoes all input.
func EchoHandler() ChannelHandler {
	return func(channel ssh.Channel, requests <-chan *ssh.Request) {
		defer channel.Close()
		go io.Copy(channel, channel)
		ssh.DiscardRequests(requests)
	}
}