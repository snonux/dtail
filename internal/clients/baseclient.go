package clients

import (
	"context"
	"io"
	"math/rand"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/discovery"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/regex"
	"github.com/mimecast/dtail/internal/ssh/client"

	gossh "golang.org/x/crypto/ssh"
)

const (
	initialRetryDelay = 2 * time.Second
	maxRetryDelay     = 60 * time.Second
	retryJitterFactor = 0.2 // +/-20% jitter to avoid synchronized reconnect storms.
)

// This is the main client data structure.
type baseClient struct {
	mu *sync.RWMutex
	config.Args
	runtime *clientRuntimeBoundary
	// To display client side stats
	stats *stats
	// We have one connection per remote server.
	connections []connectors.Connector
	// SSH auth methods to use to connect to the remote servers.
	sshAuthMethods []gossh.AuthMethod
	// authCloser owns any ssh-agent connection acquired while building the
	// auth methods; it must be closed once all SSH handshakes that consume
	// sshAuthMethods have completed.
	authCloser io.Closer
	// To deal with SSH host keys
	hostKeyCallback client.HostKeyCallback
	// Throttle how fast we initiate SSH connections concurrently
	throttleCh chan struct{}
	// Retry connection upon failure?
	retry bool
	// The current connection-wide session specification.
	sessionSpec SessionSpec
	// Connection maker helper.
	maker maker
	// Optional factory override for retry/reconnect tests.
	connectionFactory func(server string, authMethods []gossh.AuthMethod,
		hostKeyCallback client.HostKeyCallback, sessionSpec SessionSpec,
		interactive bool) connectors.Connector
	// Optional sleep override for retry tests.
	sleepFn func(context.Context, time.Duration) bool
	// Regex is the regular expresion object for line filtering
	Regex regex.Regex
}

func (c *baseClient) init() {
	dlog.Client.Debug("Initiating base client", c.Args.String())
	if c.runtime == nil {
		c.runtime = newClientRuntimeBoundary(config.CurrentRuntime())
	}

	flag := regex.Default
	if c.Args.RegexInvert {
		flag = regex.Invert
	}
	regex, err := regex.New(c.Args.RegexStr, flag)
	if err != nil {
		dlog.Client.FatalPanic(c.Regex, "Invalid regex!", err, regex)
	}
	c.Regex = regex

	if c.Args.Serverless {
		return
	}
	c.sshAuthMethods, c.hostKeyCallback, c.authCloser = client.InitSSHAuthMethods(
		c.Args.SSHAuthMethods, c.Args.SSHHostKeyCallback, c.Args.TrustAllHosts,
		c.Args.SSHPrivateKeyFilePath, c.Args.SSHAgentKeyIndex)
}

func (c *baseClient) makeConnections(maker maker) error {
	c.maker = maker
	if builder, ok := maker.(sessionSpecMaker); ok {
		sessionSpec, err := builder.makeSessionSpec()
		if err != nil {
			dlog.Client.FatalPanic("unable to build session specification", err)
		}
		c.sessionSpec = sessionSpec
	}

	discoveryService, err := discovery.New(c.Discovery, c.ServersStr, discovery.Shuffle)
	if err != nil {
		return err
	}
	for _, server := range discoveryService.ServerList() {
		c.connections = append(c.connections, c.makeConnection(server,
			c.sshAuthMethods, c.hostKeyCallback))
	}

	c.stats = newTailStats(len(c.connections), c.runtime.output, c.runtime.InterruptPause())
	return nil
}

func (c *baseClient) Start(ctx context.Context, statsCh <-chan string) (status int) {
	if c.Args.InteractiveQuery {
		return c.startInteractiveControl(ctx, statsCh)
	}
	return c.runConnections(ctx, statsCh)
}

func (c *baseClient) runConnections(ctx context.Context, statsCh <-chan string) (status int) {
	dlog.Client.Trace("Starting base client")
	// Release the ssh-agent connection (if any) once all handshakes and
	// reconnect attempts that consume c.sshAuthMethods have finished.
	if c.authCloser != nil {
		defer func() {
			if err := c.authCloser.Close(); err != nil {
				dlog.Client.Debug("baseClient", "failed to close ssh-agent connection", err)
			}
		}()
	}
	// Can be nil when serverless.
	if c.hostKeyCallback != nil {
		// Periodically check for unknown hosts, and ask the user whether to trust them or not.
		go c.hostKeyCallback.PromptAddHosts(ctx)
	}
	// Print client stats every time something on statsCh is received.
	go c.stats.Start(ctx, c.throttleCh, statsCh, c.Args.Quiet)

	var wg sync.WaitGroup
	connections := c.snapshotConnections()
	wg.Add(len(connections))
	var mutex sync.Mutex

	for i, conn := range connections {
		go func(i int, conn connectors.Connector) {
			defer wg.Done()
			connStatus := c.startConnection(ctx, i, conn)
			mutex.Lock()
			defer mutex.Unlock()
			if connStatus > status {
				status = connStatus
			}
		}(i, conn)
	}

	wg.Wait()
	return
}

func (c *baseClient) startConnection(ctx context.Context, i int,
	conn connectors.Connector) (status int) {

	retryDelay := initialRetryDelay
	retryRandom := newRetryRandom(i)

	for {
		connCtx, cancel := context.WithCancel(ctx)

		conn.Start(connCtx, cancel, c.throttleCh, c.stats.connectionsEstCh)
		cancel()
		// Retrieve status code from handler (dtail client will exit with that status)
		status = conn.Handler().Status()

		// Do we want to retry?
		if !c.retry {
			// No, we don't.
			return
		}
		select {
		case <-ctx.Done():
			// No, context is done, so no retry.
			return
		default:
		}

		// Yes, we want to retry with exponential backoff and jitter.
		sleepDuration := jitterRetryDelay(retryDelay, retryRandom)
		dlog.Client.Debug(conn.Server(), "Reconnecting", "backoff", sleepDuration)
		if !c.sleepRetry(ctx, sleepDuration) {
			return
		}

		retryDelay = nextRetryDelay(retryDelay)
		conn = c.makeConnection(conn.Server(), c.sshAuthMethods, c.hostKeyCallback)
		c.replaceConnection(i, conn)
	}
}

func nextRetryDelay(current time.Duration) time.Duration {
	if current <= 0 {
		return initialRetryDelay
	}

	next := current * 2
	if next > maxRetryDelay || next < current {
		return maxRetryDelay
	}
	return next
}

func jitterRetryDelay(base time.Duration, random *rand.Rand) time.Duration {
	if base <= 0 || random == nil {
		return base
	}

	jitter := time.Duration(float64(base) * retryJitterFactor)
	if jitter <= 0 {
		return base
	}

	minDelay := base - jitter
	maxDelay := base + jitter
	if maxDelay < minDelay {
		return base
	}

	return minDelay + time.Duration(random.Int63n(int64(maxDelay-minDelay+1)))
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func newRetryRandom(seedOffset int) *rand.Rand {
	return rand.New(rand.NewSource(time.Now().UnixNano() + int64(seedOffset)))
}

func (c *baseClient) makeConnection(server string, sshAuthMethods []gossh.AuthMethod,
	hostKeyCallback client.HostKeyCallback) connectors.Connector {
	args, sessionSpec := c.snapshotConnectionState()
	return c.makeConnectionWithState(server, sshAuthMethods, hostKeyCallback, args, sessionSpec)
}

func (c *baseClient) makeConnectionWithState(server string, sshAuthMethods []gossh.AuthMethod,
	hostKeyCallback client.HostKeyCallback, args config.Args, sessionSpec SessionSpec) connectors.Connector {
	if c.connectionFactory != nil {
		return c.connectionFactory(server, sshAuthMethods, hostKeyCallback,
			sessionSpec, args.InteractiveQuery)
	}
	if args.Serverless {
		return connectors.NewServerless(c.UserName, c.maker.makeHandler(server),
			c.maker.makeCommands(), sessionSpec, args.InteractiveQuery, c.runtime)
	}
	return connectors.NewServerConnection(server, c.UserName, sshAuthMethods,
		hostKeyCallback, c.maker.makeHandler(server), c.maker.makeCommands(),
		sessionSpec, args.InteractiveQuery, args.SSHPrivateKeyFilePath,
		args.NoAuthKey, c.runtime)
}

func (c *baseClient) sleepRetry(ctx context.Context, delay time.Duration) bool {
	if c.sleepFn != nil {
		return c.sleepFn(ctx, delay)
	}
	return sleepWithContext(ctx, delay)
}

func (c *baseClient) snapshotConnectionState() (config.Args, SessionSpec) {
	mu := c.stateMu()
	mu.RLock()
	defer mu.RUnlock()

	return c.Args, c.sessionSpec
}

func (c *baseClient) snapshotMutableState() (config.Args, SessionSpec, []connectors.Connector) {
	mu := c.stateMu()
	mu.RLock()
	defer mu.RUnlock()

	return c.Args, c.sessionSpec, append([]connectors.Connector(nil), c.connections...)
}

func (c *baseClient) snapshotConnections() []connectors.Connector {
	mu := c.stateMu()
	mu.RLock()
	defer mu.RUnlock()

	return append([]connectors.Connector(nil), c.connections...)
}

func (c *baseClient) storeReloadState(args config.Args, spec SessionSpec) {
	mu := c.stateMu()
	mu.Lock()
	defer mu.Unlock()

	c.Args = args
	c.sessionSpec = spec
}

func (c *baseClient) replaceConnection(i int, conn connectors.Connector) {
	mu := c.stateMu()
	mu.Lock()
	defer mu.Unlock()

	c.connections[i] = conn
}

func (c *baseClient) stateMu() *sync.RWMutex {
	if c.mu == nil {
		c.mu = newBaseClientMu()
	}
	return c.mu
}

func newBaseClientMu() *sync.RWMutex {
	return &sync.RWMutex{}
}
