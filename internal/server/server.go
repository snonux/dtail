package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/server/handlers"
	"github.com/mimecast/dtail/internal/ssh/server"
	user "github.com/mimecast/dtail/internal/user/server"
	"github.com/mimecast/dtail/internal/version"

	gossh "golang.org/x/crypto/ssh"
)

const sshHandshakeTimeout = 10 * time.Second

// Server is the main server data structure.
type Server struct {
	cfg          config.RuntimeConfig
	logger       logging.Logger
	readerLogger logging.Logger
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

func (s *Server) log() logging.Logger {
	if s.logger == nil {
		return logging.NopLogger{}
	}
	return s.logger
}

func (s *Server) readerLog() logging.Logger {
	if s.readerLogger == nil {
		return s.log()
	}
	return s.readerLogger
}

// secretsEqual compares two secret strings in constant time to prevent
// timing side-channel attacks. Unlike plain ==, this function takes the same
// amount of time regardless of where the first differing byte is, so an
// attacker who can measure response latency cannot recover the secret.
func secretsEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// New returns a new server.
func New(cfg config.RuntimeConfig, loggers clients.LoggerDependencies) (*Server, error) {
	logger := logging.OrNop(loggers.Server)
	if cfg.Server == nil || cfg.Common == nil {
		if fatalLogger, ok := logger.(interface{ FatalPanic(...any) }); ok {
			fatalLogger.FatalPanic("Missing runtime server/common configuration")
		}
		panic("Missing runtime server/common configuration")
	}

	logger.Info("Starting server", version.String())

	s := Server{
		cfg:          cfg,
		logger:       logger,
		readerLogger: logging.OrNop(loggers.Common),
		sshServerConfig: &gossh.ServerConfig{
			Config: gossh.Config{
				KeyExchanges: cfg.Server.KeyExchanges,
				Ciphers:      cfg.Server.Ciphers,
				MACs:         cfg.Server.MACs,
			},
		},
		stats:       newStats(cfg.Server.MaxConnections, logger),
		catLimiter:  make(chan struct{}, cfg.Server.MaxConcurrentCats),
		tailLimiter: make(chan struct{}, cfg.Server.MaxConcurrentTails),
		sched:       newScheduler(cfg, loggers),
		cont:        newContinuous(cfg, loggers),
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
		cfg.Server.AuthorizedKeysPath,
		s.authKeyStore,
		logger,
	)

	privateKey, err := server.PrivateHostKey(cfg.Server.EffectiveHostKeyPath(), cfg.Server.HostKeyBits, logger)
	if err != nil {
		return nil, fmt.Errorf("load SSH host key: %w", err)
	}
	private, err := gossh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("parse SSH host key: %w", err)
	}
	s.sshServerConfig.AddHostKey(private)

	return &s, nil
}

// Start the server.
func (s *Server) Start(ctx context.Context) (int, error) {
	s.log().Info("Starting server")
	bindAt := net.JoinHostPort(s.cfg.Server.SSHBindAddress, fmt.Sprintf("%d", s.cfg.Common.SSHPort))
	s.log().Info("Binding server", bindAt)

	listener, err := net.Listen("tcp", bindAt)
	if err != nil {
		return 1, fmt.Errorf("listen on %s: %w", bindAt, err)
	}

	go s.stats.start(ctx)
	go s.sched.start(ctx)
	go s.cont.start(ctx)
	go s.listenerLoop(ctx, listener)

	<-ctx.Done()
	// For future use.
	return 0, nil
}

func (s *Server) listenerLoop(ctx context.Context, listener net.Listener) {
	s.log().Debug("Starting listener loop")
	for {
		conn, err := listener.Accept() // Blocking
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			s.log().Error("Failed to accept incoming connection", err)
			continue
		}

		if limitErr := s.stats.serverLimitExceeded(); limitErr != nil {
			s.log().Error(limitErr)
			_ = conn.Close()
			continue
		}
		// Reserve a pre-auth slot immediately after the limit check so that
		// in-progress handshakes count against maxConnections. The slot is
		// released inside handleConnection — either promoted to a full
		// connection on success, or freed on any failure path.
		s.stats.reservePreAuth()
		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer s.recoverGoroutinePanic("SSH connection", conn.RemoteAddr(), nil)
	defer func() { _ = conn.Close() }()

	s.log().Info("Handling connection")

	// The caller (listenerLoop) already reserved a pre-auth slot via
	// reservePreAuth. We must release it on every early-exit path. On the
	// happy path we promote it atomically to a full connection instead.
	preAuthReleased := false
	releasePreAuth := func() {
		if !preAuthReleased {
			s.stats.releasePreAuth()
			preAuthReleased = true
		}
	}
	defer releasePreAuth()

	// Prevent slow clients from holding connections open indefinitely before SSH handshake completes.
	if deadlineErr := conn.SetDeadline(time.Now().Add(sshHandshakeTimeout)); deadlineErr != nil {
		s.log().Error("Failed to set SSH handshake deadline", deadlineErr)
		_ = conn.Close()
		releasePreAuth()
		return
	}

	activeConn := newActivityConn(conn)
	sshConn, chans, reqs, err := gossh.NewServerConn(activeConn, s.sshServerConfig)
	if err != nil {
		// Handshake failed (auth error, timeout, or connection reset).
		// Release the pre-auth slot so the limit accurately reflects reality.
		s.log().Error("SSH handshake failed", err)
		releasePreAuth()
		return
	}

	// Handshake succeeded. Replace the fixed handshake deadline with a rolling
	// inactivity deadline that successful reads and writes refresh.
	idleTimeout := time.Duration(s.cfg.Server.IdleSessionTimeoutS) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = time.Duration(config.DefaultIdleSessionTimeoutS) * time.Second
	}
	if deadlineErr := activeConn.enable(idleTimeout); deadlineErr != nil {
		s.log().Error("Failed to set SSH idle session deadline", deadlineErr)
		_ = sshConn.Close()
		return
	}

	// Atomically convert the pre-auth reservation into a full authenticated
	// connection. This ensures no instant where neither counter holds the slot,
	// keeping the effective connection count consistent with maxConnections.
	s.stats.promotePreAuthToConnection()
	preAuthReleased = true // pre-auth slot was consumed by promote; guard is no longer needed
	defer s.stats.decrementConnections()

	go func() {
		defer s.recoverGoroutinePanic("SSH global request", sshConn.RemoteAddr(), func() { _ = sshConn.Close() })
		gossh.DiscardRequests(reqs)
	}()
	for newChannel := range chans {
		go s.handleChannel(ctx, sshConn, newChannel)
	}
}

func (s *Server) handleChannel(ctx context.Context, sshConn gossh.Conn,
	newChannel gossh.NewChannel) {
	defer s.recoverGoroutinePanic("SSH channel", sshConn.RemoteAddr(), func() { _ = sshConn.Close() })

	serverUser, userErr := user.New(sshConn.User(), sshConn.RemoteAddr().String(), s.cfg.Server.UserPermissions, s.log())
	if userErr != nil {
		s.log().Error(serverUser, userErr)
		if rejectErr := newChannel.Reject(gossh.Prohibited, userErr.Error()); rejectErr != nil {
			s.log().Debug(rejectErr)
		}
		return
	}

	s.log().Info(serverUser, "Invoking channel handler")
	if newChannel.ChannelType() != "session" {
		channelTypeErr := errors.New("don't allow channel types other than session")
		s.log().Error(serverUser, channelTypeErr)
		if rejectErr := newChannel.Reject(gossh.Prohibited, channelTypeErr.Error()); rejectErr != nil {
			s.log().Debug(rejectErr)
		}
		return
	}

	channel, requests, acceptErr := newChannel.Accept()
	if acceptErr != nil {
		s.log().Error(serverUser, "Could not accept channel", acceptErr)
		return
	}

	if err := s.handleRequests(ctx, sshConn, requests, channel, serverUser); err != nil {
		s.log().Error(serverUser, err)
		_ = sshConn.Close()
	}
}

func (s *Server) handleRequests(ctx context.Context, sshConn gossh.Conn,
	in <-chan *gossh.Request, channel gossh.Channel, user *user.User) error {

	s.log().Info(user, "Invoking request handler")
	for req := range in {
		var payload = struct{ Value string }{}
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
			s.log().Error(user, err)
		}

		switch req.Type {
		case "shell":
			s.handleShellRequest(ctx, sshConn, channel, user, req)
		default:
			if err := req.Reply(false, nil); err != nil {
				s.log().Trace(user, fmt.Errorf("reply(false): %w", err))
			}
			return fmt.Errorf("closing SSH connection as unknown request received|%s|%v",
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
	var err error
	switch user.Name {
	case config.HealthUser:
		handler, err = handlers.NewHealthHandler(user, s.log())
	default:
		handler, err = handlers.NewServerHandler(
			user,
			s.catLimiter,
			s.tailLimiter,
			s.cfg.Server,
			s.authKeyStore,
			nil,
			handlers.HandlerLoggers{Diagnostics: s.log(), Reader: s.readerLog()},
		)
	}
	if err != nil {
		s.log().Error(user, "Unable to create session handler", err)
		if replyErr := req.Reply(false, nil); replyErr != nil {
			s.log().Trace(user, fmt.Errorf("reply(false): %w", replyErr))
		}
		if closeErr := sshConn.Close(); closeErr != nil {
			s.log().Trace(user, fmt.Errorf("close failed session connection: %w", closeErr))
		}
		return
	}

	// Publish the transport reader before either I/O goroutine can dispatch
	// commands. Input and output goroutines start independently; without this
	// attachment signal, a fast command can reach shutdown before Read starts
	// and incorrectly look like a serverless/raw-channel consumer.
	if outputReader, ok := handler.(interface{ AttachOutputReader() }); ok {
		outputReader.AttachOutputReader()
	}

	var terminateOnce sync.Once
	terminate := func() {
		defer s.recoverGoroutinePanic("session termination", user, func() { _ = sshConn.Close() })
		terminateOnce.Do(func() {
			defer func() {
				if closeErr := sshConn.Close(); closeErr != nil {
					s.log().Trace(user, fmt.Errorf("close session connection: %w", closeErr))
				}
			}()
			handler.Shutdown()
		})
	}

	// Start goroutine to copy data from channel to handler
	go func() {
		defer terminate()
		defer s.recoverGoroutinePanic("session input", user, func() { _ = sshConn.Close() })
		if _, copyErr := io.Copy(channel, handler); copyErr != nil {
			s.log().Trace(user, fmt.Errorf("channel->handler: %w", copyErr))
		}
	}()

	// Start goroutine to copy data from handler to channel
	go func() {
		defer terminate()
		defer s.recoverGoroutinePanic("session output", user, func() { _ = sshConn.Close() })
		if _, copyErr := io.Copy(handler, channel); copyErr != nil {
			s.log().Trace(user, fmt.Errorf("handler->channel: %w", copyErr))
		}
	}()

	// Start goroutine to handle context or handler completion
	go func() {
		defer terminate()
		defer s.recoverGoroutinePanic("session lifecycle", user, func() { _ = sshConn.Close() })
		select {
		case <-ctx.Done():
		case <-handler.Done():
		}
	}()

	// Start goroutine to handle connection lifecycle and cleanup.
	// Note: connection-counter management (increment/decrement) is done in
	// handleConnection via defer, not here, so that the counter is balanced
	// 1:1 per TCP connection regardless of how many shell requests are opened.
	go func() {
		defer terminate()
		defer s.recoverGoroutinePanic("session connection wait", user, func() { _ = sshConn.Close() })
		if waitErr := sshConn.Wait(); waitErr != nil && !errors.Is(waitErr, io.EOF) {
			s.log().Error(user, waitErr)
		}
		s.log().Info(user, "Good bye Mister!")
	}()

	// Reply to indicate shell request was accepted
	if replyErr := req.Reply(true, nil); replyErr != nil {
		s.log().Trace(user, fmt.Errorf("reply(true): %w", replyErr))
	}
}

// Callback for SSH authentication.
func (s *Server) Callback(c gossh.ConnMetadata,
	authPayload []byte) (*gossh.Permissions, error) {

	authenticatedUser, err := user.New(c.User(), c.RemoteAddr().String(), s.cfg.Server.UserPermissions, s.log())
	if err != nil {
		return nil, err
	}

	authInfo := string(authPayload)
	remoteAddr := c.RemoteAddr().String()
	remoteIP, _, splitErr := net.SplitHostPort(remoteAddr)
	if splitErr != nil {
		s.log().Debug(authenticatedUser, "Unable to split remote address host/port, using raw address",
			"remoteAddr", remoteAddr, "error", splitErr)
		remoteIP = remoteAddr
	}

	if strategy, found := s.authStrategies[authenticatedUser.Name]; found && strategy(authenticatedUser, authInfo, remoteIP) {
		return nil, nil
	}

	return nil, fmt.Errorf("user %s not authorized", authenticatedUser)
}

func (s *Server) newAuthStrategies() map[string]authStrategy {
	return map[string]authStrategy{
		config.HealthUser:     s.authorizeHealthUser,
		config.ScheduleUser:   s.authorizeScheduleUser,
		config.ContinuousUser: s.authorizeContinuousUser,
	}
}

func (s *Server) authorizeHealthUser(user *user.User, authInfo, _ string) bool {
	// Use constant-time comparison to avoid timing side-channel attacks.
	// An attacker who can measure response latency must not be able to infer
	// how many bytes of the secret matched.
	if !secretsEqual(authInfo, config.HealthUser) {
		return false
	}
	s.log().Debug(user, "Granting permissions to health user")
	return true
}

func (s *Server) authorizeScheduleUser(user *user.User, authInfo, remoteIP string) bool {
	for i := range s.cfg.Server.Schedule {
		job := &s.cfg.Server.Schedule[i]
		if s.backgroundCanSSH(user, authInfo, remoteIP, job.Name, job.AllowFrom) {
			s.log().Debug(user, "Granting SSH connection")
			return true
		}
	}
	return false
}

func (s *Server) authorizeContinuousUser(user *user.User, authInfo, remoteIP string) bool {
	for i := range s.cfg.Server.Continuous {
		job := &s.cfg.Server.Continuous[i]
		if s.backgroundCanSSH(user, authInfo, remoteIP, job.Name, job.AllowFrom) {
			s.log().Debug(user, "Granting SSH connection")
			return true
		}
	}
	return false
}

// backgroundCanSSH checks whether a background SSH connection is authorised.
// The caller passes authInfo (the client-presented password/secret) and
// allowedJobName (the operator-configured value from the server config).
//
// Security notes:
//   - The secret comparison MUST use secretsEqual (crypto/subtle) to prevent
//     timing side-channel attacks; do NOT revert to plain ==.
//   - authInfo/jobName MUST NOT appear in any log line — if debug logging is
//     ever enabled in production the shared secret would leak. Log only the
//     operator-visible allowedJobName or a fixed placeholder.
func (s *Server) backgroundCanSSH(user *user.User, authInfo, remoteIP,
	allowedJobName string, allowFrom []string) bool {

	// Do not log authInfo (the client-presented secret) — only log the
	// operator-configured job name so the shared secret cannot leak into
	// debug logs.
	s.log().Debug("backgroundCanSSH", user, remoteIP, "allowedJobName", allowedJobName, allowFrom)

	// Constant-time comparison prevents a remote attacker from recovering the
	// secret by measuring how long the server takes to reject wrong values.
	if !secretsEqual(authInfo, allowedJobName) {
		s.log().Debug(user, "backgroundCanSSH",
			"Job name does not match, skipping to next one...", "allowedJobName", allowedJobName)
		return false
	}

	for _, myAddr := range allowFrom {
		ips, err := net.LookupIP(myAddr)
		if err != nil {
			s.log().Debug(user, "backgroundCanSSH", "Unable to lookup IP "+
				"address for allowed hosts lookup, skipping to next one...",
				"allowedJobName", allowedJobName, "addr", myAddr, "error", err)
			continue
		}
		for _, ip := range ips {
			s.log().Debug(user, "backgroundCanSSH", "Comparing IP addresses",
				"allowedJobName", allowedJobName, "remoteIP", remoteIP, "candidateIP", ip.String())
			if remoteIP == ip.String() {
				return true
			}
		}
	}

	return false
}
