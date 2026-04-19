package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/server/handlers"
	"github.com/mimecast/dtail/internal/ssh/server"
	user "github.com/mimecast/dtail/internal/user/server"
	"github.com/mimecast/dtail/internal/version"

	gossh "golang.org/x/crypto/ssh"
)

const sshHandshakeTimeout = 10 * time.Second

// Server is the main server data structure.
type Server struct {
	cfg config.RuntimeConfig
	// Various server statistics counters.
	stats stats
	// SSH server configuration.
	sshServerConfig *gossh.ServerConfig
	// To control the max amount of concurrent cats.
	catLimiter chan struct{}
	// To control the max amount of concurrent tails.
	tailLimiter chan struct{}
	// To run scheduled tasks (if configured)
	sched *scheduler
	// Mointor log files for pattern (if configured)
	cont *continuous
	// Authentication strategies keyed by SSH username.
	authStrategies map[string]authStrategy
	// In-memory auth key cache for fast reconnect.
	authKeyStore *server.AuthKeyStore
}

type authStrategy func(*user.User, string, string) bool

// New returns a new server.
func New(cfg config.RuntimeConfig) *Server {
	if cfg.Server == nil || cfg.Common == nil {
		dlog.Server.FatalPanic("Missing runtime server/common configuration")
	}

	dlog.Server.Info("Starting server", version.String())

	s := Server{
		cfg: cfg,
		sshServerConfig: &gossh.ServerConfig{
			Config: gossh.Config{
				KeyExchanges: cfg.Server.KeyExchanges,
				Ciphers:      cfg.Server.Ciphers,
				MACs:         cfg.Server.MACs,
			},
		},
		stats:       newStats(cfg.Server.MaxConnections),
		catLimiter:  make(chan struct{}, cfg.Server.MaxConcurrentCats),
		tailLimiter: make(chan struct{}, cfg.Server.MaxConcurrentTails),
		sched:       newScheduler(cfg),
		cont:        newContinuous(cfg),
		authKeyStore: server.NewAuthKeyStore(
			time.Duration(cfg.Server.AuthKeyTTLSeconds)*time.Second,
			cfg.Server.AuthKeyMaxPerUser,
		),
	}
	s.authStrategies = s.newAuthStrategies()

	s.sshServerConfig.PasswordCallback = s.Callback
	s.sshServerConfig.PublicKeyCallback = server.NewPublicKeyCallback(
		cfg.Server.AuthKeyEnabled,
		cfg.Common.CacheDir,
		s.authKeyStore,
	)

	private, err := gossh.ParsePrivateKey(server.PrivateHostKey(cfg.Server.HostKeyFile, cfg.Server.HostKeyBits))
	if err != nil {
		dlog.Server.FatalPanic(err)
	}
	s.sshServerConfig.AddHostKey(private)

	return &s
}

// Start the server.
func (s *Server) Start(ctx context.Context) int {
	dlog.Server.Info("Starting server")
	bindAt := fmt.Sprintf("%s:%d", s.cfg.Server.SSHBindAddress, s.cfg.Common.SSHPort)
	dlog.Server.Info("Binding server", bindAt)

	listener, err := net.Listen("tcp", bindAt)
	if err != nil {
		dlog.Server.FatalPanic("Failed to open listening TCP socket", err)
	}

	go s.stats.start(ctx)
	go s.sched.start(ctx)
	go s.cont.start(ctx)
	go s.listenerLoop(ctx, listener)

	<-ctx.Done()
	// For future use.
	return 0
}

func (s *Server) listenerLoop(ctx context.Context, listener net.Listener) {
	dlog.Server.Debug("Starting listener loop")
	for {
		conn, err := listener.Accept() // Blocking
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			dlog.Server.Error("Failed to accept incoming connection", err)
			continue
		}

		if err := s.stats.serverLimitExceeded(); err != nil {
			dlog.Server.Error(err)
			conn.Close()
			continue
		}
		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	dlog.Server.Info("Handling connection")

	// Prevent slow clients from holding connections open indefinitely before SSH handshake completes.
	if err := conn.SetDeadline(time.Now().Add(sshHandshakeTimeout)); err != nil {
		dlog.Server.Error("Failed to set SSH handshake deadline", err)
		conn.Close()
		return
	}

	sshConn, chans, reqs, err := gossh.NewServerConn(conn, s.sshServerConfig)
	if err != nil {
		dlog.Server.Error("Something just happened", err)
		return
	}

	// Handshake succeeded; remove deadline so active sessions are not cut off by the handshake timeout.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		dlog.Server.Error("Failed to clear SSH handshake deadline", err)
		sshConn.Close()
		return
	}

	// Increment once per TCP connection and decrement via defer so the
	// counter is always balanced regardless of how many SSH channels or
	// shell requests are multiplexed over this connection.
	s.stats.incrementConnections()
	defer s.stats.decrementConnections()

	go gossh.DiscardRequests(reqs)
	for newChannel := range chans {
		go s.handleChannel(ctx, sshConn, newChannel)
	}
}

func (s *Server) handleChannel(ctx context.Context, sshConn gossh.Conn,
	newChannel gossh.NewChannel) {

	user, err := user.New(sshConn.User(), sshConn.RemoteAddr().String(), s.cfg.Server.UserPermissions)
	if err != nil {
		dlog.Server.Error(user, err)
		if err := newChannel.Reject(gossh.Prohibited, err.Error()); err != nil {
			dlog.Server.Debug(err)
		}
		return
	}

	dlog.Server.Info(user, "Invoking channel handler")
	if newChannel.ChannelType() != "session" {
		err := errors.New("Don'w allow other channel types than session")
		dlog.Server.Error(user, err)
		if err := newChannel.Reject(gossh.Prohibited, err.Error()); err != nil {
			dlog.Server.Debug(err)
		}
		return
	}

	channel, requests, err := newChannel.Accept()
	if err != nil {
		dlog.Server.Error(user, "Could not accept channel", err)
		return
	}

	if err := s.handleRequests(ctx, sshConn, requests, channel, user); err != nil {
		dlog.Server.Error(user, err)
		sshConn.Close()
	}
}

func (s *Server) handleRequests(ctx context.Context, sshConn gossh.Conn,
	in <-chan *gossh.Request, channel gossh.Channel, user *user.User) error {

	dlog.Server.Info(user, "Invoking request handler")
	for req := range in {
		var payload = struct{ Value string }{}
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
			dlog.Server.Error(user, err)
		}

		switch req.Type {
		case "shell":
			s.handleShellRequest(ctx, sshConn, channel, user, req)
		default:
			if err := req.Reply(false, nil); err != nil {
				dlog.Server.Trace(user, fmt.Errorf("reply(false): %w", err))
			}
			return fmt.Errorf("Closing SSH connection as unknown request received|%s|%v",
				req.Type, payload.Value)
		}
	}
	return nil
}

// handleShellRequest sets up the shell session with handler goroutines for I/O,
// context cancellation, and connection lifecycle management.
func (s *Server) handleShellRequest(ctx context.Context, sshConn gossh.Conn,
	channel gossh.Channel, user *user.User, req *gossh.Request) {

	// Create the appropriate handler based on user type
	var handler handlers.Handler
	switch user.Name {
	case config.HealthUser:
		handler = handlers.NewHealthHandler(user)
	default:
		handler = handlers.NewServerHandler(
			user,
			s.catLimiter,
			s.tailLimiter,
			s.cfg.Server,
			s.authKeyStore,
		)
	}

	terminate := func() {
		handler.Shutdown()
		sshConn.Close()
	}

	// Start goroutine to copy data from channel to handler
	go func() {
		defer terminate()
		if _, err := io.Copy(channel, handler); err != nil {
			dlog.Server.Trace(user, fmt.Errorf("channel->handler: %w", err))
		}
	}()

	// Start goroutine to copy data from handler to channel
	go func() {
		defer terminate()
		if _, err := io.Copy(handler, channel); err != nil {
			dlog.Server.Trace(user, fmt.Errorf("handler->channel: %w", err))
		}
	}()

	// Start goroutine to handle context or handler completion
	go func() {
		select {
		case <-ctx.Done():
		case <-handler.Done():
		}
		terminate()
	}()

	// Start goroutine to handle connection lifecycle and cleanup.
	// Note: connection-counter management (increment/decrement) is done in
	// handleConnection via defer, not here, so that the counter is balanced
	// 1:1 per TCP connection regardless of how many shell requests are opened.
	go func() {
		if err := sshConn.Wait(); err != nil && err != io.EOF {
			dlog.Server.Error(user, err)
		}
		dlog.Server.Info(user, "Good bye Mister!")
		terminate()
	}()

	// Reply to indicate shell request was accepted
	if err := req.Reply(true, nil); err != nil {
		dlog.Server.Trace(user, fmt.Errorf("reply(true): %w", err))
	}
}

// Callback for SSH authentication.
func (s *Server) Callback(c gossh.ConnMetadata,
	authPayload []byte) (*gossh.Permissions, error) {

	user, err := user.New(c.User(), c.RemoteAddr().String(), s.cfg.Server.UserPermissions)
	if err != nil {
		return nil, err
	}

	authInfo := string(authPayload)
	remoteAddr := c.RemoteAddr().String()
	remoteIP, _, splitErr := net.SplitHostPort(remoteAddr)
	if splitErr != nil {
		dlog.Server.Debug(user, "Unable to split remote address host/port, using raw address",
			"remoteAddr", remoteAddr, "error", splitErr)
		remoteIP = remoteAddr
	}

	if strategy, found := s.authStrategies[user.Name]; found && strategy(user, authInfo, remoteIP) {
		return nil, nil
	}

	return nil, fmt.Errorf("user %s not authorized", user)
}

func (s *Server) newAuthStrategies() map[string]authStrategy {
	return map[string]authStrategy{
		config.HealthUser:     s.authorizeHealthUser,
		config.ScheduleUser:   s.authorizeScheduleUser,
		config.ContinuousUser: s.authorizeContinuousUser,
	}
}

func (s *Server) authorizeHealthUser(user *user.User, authInfo, _ string) bool {
	if authInfo != config.HealthUser {
		return false
	}
	dlog.Server.Debug(user, "Granting permissions to health user")
	return true
}

func (s *Server) authorizeScheduleUser(user *user.User, authInfo, remoteIP string) bool {
	for i := range s.cfg.Server.Schedule {
		job := &s.cfg.Server.Schedule[i]
		if s.backgroundCanSSH(user, authInfo, remoteIP, job.Name, job.AllowFrom) {
			dlog.Server.Debug(user, "Granting SSH connection")
			return true
		}
	}
	return false
}

func (s *Server) authorizeContinuousUser(user *user.User, authInfo, remoteIP string) bool {
	for i := range s.cfg.Server.Continuous {
		job := &s.cfg.Server.Continuous[i]
		if s.backgroundCanSSH(user, authInfo, remoteIP, job.Name, job.AllowFrom) {
			dlog.Server.Debug(user, "Granting SSH connection")
			return true
		}
	}
	return false
}

func (s *Server) backgroundCanSSH(user *user.User, jobName, remoteIP,
	allowedJobName string, allowFrom []string) bool {

	dlog.Server.Debug("backgroundCanSSH", user, jobName, remoteIP, allowedJobName, allowFrom)
	if jobName != allowedJobName {
		dlog.Server.Debug(user, jobName, "backgroundCanSSH",
			"Job name does not match, skipping to next one...", allowedJobName)
		return false
	}

	for _, myAddr := range allowFrom {
		ips, err := net.LookupIP(myAddr)
		if err != nil {
			dlog.Server.Debug(user, jobName, "backgroundCanSSH", "Unable to lookup IP "+
				"address for allowed hosts lookup, skipping to next one...", myAddr, err)
			continue
		}
		for _, ip := range ips {
			dlog.Server.Debug(user, jobName, "backgroundCanSSH", "Comparing IP addresses",
				remoteIP, ip.String())
			if remoteIP == ip.String() {
				return true
			}
		}
	}

	return false
}
