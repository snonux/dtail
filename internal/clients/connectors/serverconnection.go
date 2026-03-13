package connectors

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/ssh/client"

	"golang.org/x/crypto/ssh"
)

// SSHSettings provides the connection settings needed by ServerConnection.
type SSHSettings interface {
	SSHPort() int
	SSHConnectTimeout() time.Duration
}

const (
	defaultSSHConnectTimeout = 2 * time.Second
	defaultSSHPort           = 2222
	defaultCapabilityWait    = 250 * time.Millisecond
)

// ServerConnection represents a connection to a single remote dtail server via
// SSH protocol.
type ServerConnection struct {
	// The full server string as received from the server discovery (can be with port number)
	server string
	// Only the hostname or FQDN (without the port number)
	hostname string
	// Only the port number.
	port            int
	config          *ssh.ClientConfig
	handler         handlers.Handler
	commands        []string
	authKeyPath     string
	authKeyDisabled bool
	hostKeyCallback client.HostKeyCallback
	throttlingDone  bool
}

var _ Connector = (*ServerConnection)(nil)

// NewServerConnection returns a new DTail SSH server connection.
func NewServerConnection(server string, userName string,
	authMethods []ssh.AuthMethod, hostKeyCallback client.HostKeyCallback,
	handler handlers.Handler, commands []string, authKeyPath string,
	authKeyDisabled bool, settings SSHSettings) *ServerConnection {

	dlog.Client.Debug(server, "Creating new connection", server, handler, commands)
	sshConnectTimeout := defaultSSHConnectTimeout
	defaultPort := defaultSSHPort
	if settings != nil {
		sshConnectTimeout = settings.SSHConnectTimeout()
		defaultPort = settings.SSHPort()
	}
	if sshConnectTimeout <= 0 {
		sshConnectTimeout = defaultSSHConnectTimeout
	}
	if defaultPort <= 0 {
		defaultPort = defaultSSHPort
	}

	c := ServerConnection{
		hostKeyCallback: hostKeyCallback,
		server:          server,
		handler:         handler,
		commands:        commands,
		authKeyPath:     resolveAuthKeyPath(authKeyPath),
		authKeyDisabled: authKeyDisabled,
		config: &ssh.ClientConfig{
			User:            userName,
			Auth:            authMethods,
			HostKeyCallback: hostKeyCallback.Wrap(),
			Timeout:         sshConnectTimeout,
		},
	}

	c.initServerPort(defaultPort)
	return &c
}

// Server returns the server hostname connected to.
func (c *ServerConnection) Server() string { return c.server }

// Handler returns the handler used for the connection.
func (c *ServerConnection) Handler() handlers.Handler { return c.handler }

// SupportsQueryUpdates reports whether the remote server advertised the
// runtime query replacement capability. Older servers simply time out and
// return false here without affecting the legacy command path.
func (c *ServerConnection) SupportsQueryUpdates(timeout time.Duration) bool {
	return supportsQueryUpdates(c.handler, timeout)
}

// Attempt to parse the server port address from the provided server FQDN.
func (c *ServerConnection) initServerPort(defaultPort int) {
	parts := strings.Split(c.server, ":")
	if len(parts) == 1 {
		c.hostname = c.server
		c.port = defaultPort
		return
	}

	dlog.Client.Debug("Parsing port from hostname", parts)
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		dlog.Client.FatalPanic("Unable to parse client port", c.server, parts, err)
	}
	c.hostname = parts[0]
	c.port = port
}

// Start the connection to the server.
func (c *ServerConnection) Start(ctx context.Context, cancel context.CancelFunc,
	throttleCh, statsCh chan struct{}) {

	// Throttle how many connections can be established concurrently (based on ch length)
	dlog.Client.Debug(c.server, "Throttling connection", len(throttleCh), cap(throttleCh))

	select {
	case throttleCh <- struct{}{}:
	case <-ctx.Done():
		dlog.Client.Debug(c.server, "Not establishing connection as context is done",
			len(throttleCh), cap(throttleCh))
		return
	}

	dlog.Client.Debug(c.server, "Throttling says that the connection can be established",
		len(throttleCh), cap(throttleCh))

	go func() {
		defer func() {
			if !c.throttlingDone {
				dlog.Client.Debug(c.server, "Unthrottling connection (1)",
					len(throttleCh), cap(throttleCh))
				c.throttlingDone = true
				<-throttleCh
			}
			cancel()
		}()

		if err := c.dial(ctx, cancel, throttleCh, statsCh); err != nil {
			dlog.Client.Warn(c.server, err)
			if c.hostKeyCallback.Untrusted(c.server) {
				dlog.Client.Debug(c.server, "Not trusting host")
			}
		}
	}()

	<-ctx.Done()
}

// Dail into a new SSH connection. Close connection in case of an error.
func (c *ServerConnection) dial(ctx context.Context, cancel context.CancelFunc,
	throttleCh, statsCh chan struct{}) error {

	dlog.Client.Debug(c.server, "Incrementing connection stats")
	statsCh <- struct{}{}
	defer func() {
		dlog.Client.Debug(c.server, "Decrementing connection stats")
		<-statsCh
	}()

	address := fmt.Sprintf("%s:%d", c.hostname, c.port)
	dlog.Client.Debug(c.server, "Dialing into the connection", address)

	// Use context-aware dialing to enable proper cancellation during connection establishment.
	// TCP KeepAlive (30s) prevents silent connection failures on long-lived connections.
	dialer := &net.Dialer{
		Timeout:   c.config.Timeout, // Use the SSH config timeout (2 seconds)
		KeepAlive: 30 * time.Second, // Standard Go default for connection health monitoring
	}

	// Establish TCP connection with context support for cancellation
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("failed to dial TCP connection to %s: %w", address, err)
	}

	// Perform SSH handshake over the established TCP connection
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, c.config)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SSH handshake failed for %s: %w", address, err)
	}

	// Create SSH client from the connection components
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	return c.session(ctx, cancel, client, throttleCh)
}

// Create the SSH session. Close the session in case of an error.
func (c *ServerConnection) session(ctx context.Context, cancel context.CancelFunc,
	client *ssh.Client, throttleCh chan struct{}) error {

	dlog.Client.Debug(c.server, "Creating SSH session")
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session for %s: %w", c.server, err)
	}
	defer session.Close()
	return c.handle(ctx, cancel, session, throttleCh)
}

func (c *ServerConnection) handle(ctx context.Context, cancel context.CancelFunc,
	session *ssh.Session, throttleCh chan struct{}) error {

	dlog.Client.Debug(c.server, "Creating handler for SSH session")
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get SSH session stdin pipe for %s: %w", c.server, err)
	}
	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get SSH session stdout pipe for %s: %w", c.server, err)
	}
	if err := session.Shell(); err != nil {
		return fmt.Errorf("failed to start SSH shell for %s: %w", c.server, err)
	}

	go func() {
		defer cancel()
		if _, err := io.Copy(stdinPipe, c.handler); err != nil {
			dlog.Client.Trace(err)
		}
	}()
	go func() {
		defer cancel()
		if _, err := io.Copy(c.handler, stdoutPipe); err != nil {
			dlog.Client.Trace(err)
		}
	}()
	go func() {
		defer cancel()
		select {
		case <-c.handler.Done():
		case <-ctx.Done():
		}
	}()

	if c.authKeyDisabled {
		dlog.Client.Debug(c.server, "Skipping AUTHKEY registration because auth-key is disabled")
	} else {
		c.sendAuthKeyRegistrationCommand()
	}

	// Send all requested commands to the server.
	for _, command := range c.commands {
		dlog.Client.Debug(command)
		if err := c.handler.SendMessage(command); err != nil {
			dlog.Client.Debug(err)
		}
	}

	if !c.throttlingDone {
		dlog.Client.Debug(c.server, "Unthrottling connection (2)",
			len(throttleCh), cap(throttleCh))
		c.throttlingDone = true
		<-throttleCh
	}

	<-ctx.Done()
	c.handler.Shutdown()
	return nil
}

func resolveAuthKeyPath(authKeyPath string) string {
	if strings.TrimSpace(authKeyPath) != "" {
		return authKeyPath
	}
	return os.Getenv("HOME") + "/.ssh/id_rsa"
}

func (c *ServerConnection) sendAuthKeyRegistrationCommand() {
	authKeyPubPath := c.authKeyPath + ".pub"
	authKeyPubBytes, err := os.ReadFile(authKeyPubPath)
	if err != nil {
		dlog.Client.Debug(c.server, "Skipping AUTHKEY registration, unable to read public key", authKeyPubPath, err)
		return
	}

	authKeyBase64, err := extractAuthKeyBase64(authKeyPubBytes)
	if err != nil {
		dlog.Client.Debug(c.server, "Skipping AUTHKEY registration, invalid public key file", authKeyPubPath, err)
		return
	}

	if err := c.handler.SendMessage("AUTHKEY " + authKeyBase64); err != nil {
		dlog.Client.Debug(c.server, "Unable to send AUTHKEY registration command", err)
		return
	}
	dlog.Client.Debug(c.server, "Sent AUTHKEY registration command", authKeyPubPath)
}

func extractAuthKeyBase64(authKeyPubBytes []byte) (string, error) {
	authKeyPubContent := string(authKeyPubBytes)
	for _, line := range strings.Split(authKeyPubContent, "\n") {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") {
			continue
		}

		fields := strings.Fields(trimmedLine)
		if len(fields) < 2 {
			return "", fmt.Errorf("expected authorized key format '<type> <base64-key> [comment]'")
		}

		authKeyBase64 := strings.TrimSpace(fields[1])
		if _, err := base64.StdEncoding.DecodeString(authKeyBase64); err != nil {
			return "", fmt.Errorf("invalid base64 public key: %w", err)
		}

		return authKeyBase64, nil
	}

	return "", fmt.Errorf("no public key found")
}

func supportsQueryUpdates(handler handlers.Handler, timeout time.Duration) bool {
	if handler == nil {
		return false
	}

	if timeout <= 0 {
		timeout = defaultCapabilityWait
	}
	if !handler.WaitForCapabilities(timeout) {
		return false
	}

	return handler.HasCapability(protocol.CapabilityQueryUpdateV1)
}
