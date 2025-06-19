package clients

import (
	"context"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/constants"
	"github.com/mimecast/dtail/internal/discovery"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/regex"
	"github.com/mimecast/dtail/internal/ssh/client"

	gossh "golang.org/x/crypto/ssh"
)

// retryTimer is a reusable timer for connection retry delays, providing
// performance optimization by avoiding repeated timer allocations.
var retryTimer = time.NewTimer(constants.RetryTimerDuration)

// baseClient is the foundational client structure that provides common functionality
// for all DTail client types. It manages SSH connections, server discovery, authentication,
// connection throttling, and retry logic. All specific client implementations (TailClient,
// CatClient, etc.) embed this structure to inherit core client capabilities.
//
// The baseClient supports both server-based operations (via SSH) and serverless
// operations (local file processing), determined by the Serverless configuration flag.
type baseClient struct {
	// Embedded configuration arguments containing all client settings
	config.Args
	
	// stats manages and displays real-time client statistics such as
	// connection counts, data transfer rates, and operation metrics
	stats *stats
	
	// servers contains the list of remote DTail servers to connect to,
	// populated through server discovery mechanisms
	servers []string
	
	// connections maintains one connector per remote server, handling
	// the actual communication channel (SSH or serverless)
	connections []connectors.Connector
	
	// sshAuthMethods contains the SSH authentication methods to use
	// when connecting to remote servers (keys, passwords, etc.)
	sshAuthMethods []gossh.AuthMethod
	
	// hostKeyCallback handles SSH host key verification, managing
	// known hosts and user prompts for unknown servers
	hostKeyCallback client.HostKeyCallback
	
	// throttleCh controls the rate of concurrent SSH connection attempts
	// to prevent overwhelming remote servers or network infrastructure
	throttleCh chan struct{}
	
	// retry determines whether the client should automatically retry
	// failed connections, useful for long-running operations
	retry bool
	
	// maker is a factory interface for creating handlers and commands
	// specific to each client type (tail, cat, grep, mapr, health)
	maker maker
	
	// Regex is the compiled regular expression used for line filtering
	// across all connected servers, supporting both normal and inverted matching
	Regex regex.Regex
}

// init initializes the base client by compiling the regular expression
// and setting up SSH authentication methods. This method must be called
// before making connections or starting client operations.
//
// The initialization process:
// 1. Compiles the regex pattern with appropriate flags (normal/inverted)
// 2. Sets up SSH authentication methods if not in serverless mode
// 3. Configures host key verification callbacks
func (c *baseClient) init() {
	dlog.Client.Debug("Initiating base client", c.Args.String())

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
	c.sshAuthMethods, c.hostKeyCallback = client.InitSSHAuthMethods(
		c.Args.SSHAuthMethods, c.Args.SSHHostKeyCallback, c.Args.TrustAllHosts,
		c.throttleCh, c.Args.SSHPrivateKeyFilePath)
}

// makeConnections creates connections to all discovered servers using the
// provided maker factory. This method performs server discovery, creates
// appropriate connectors (SSH or serverless), and initializes client statistics.
//
// Parameters:
//   maker: Factory interface for creating handlers and commands specific to the client type
//
// The connection creation process:
// 1. Discovers servers using the configured discovery service
// 2. Creates a connector for each discovered server
// 3. Initializes statistics tracking for all connections
func (c *baseClient) makeConnections(maker maker) {
	c.maker = maker

	discoveryService := discovery.New(c.Discovery, c.ServersStr, discovery.Shuffle)
	for _, server := range discoveryService.ServerList() {
		c.connections = append(c.connections, c.makeConnection(server,
			c.sshAuthMethods, c.hostKeyCallback))
	}

	c.stats = newTailStats(len(c.connections))
}

// Start begins the client operation by launching connections to all servers
// concurrently. This method coordinates the entire client lifecycle including
// connection management, statistics reporting, and graceful shutdown.
//
// Parameters:
//   ctx: Context for cancellation and timeout control  
//   statsCh: Channel for receiving statistics display requests
//
// Returns:
//   int: The highest status code returned by any connection (0=success, >0=error)
//
// The start process:
// 1. Launches host key verification prompts if needed
// 2. Starts statistics reporting in a separate goroutine
// 3. Creates a goroutine for each server connection
// 4. Waits for all connections to complete and returns the worst status
func (c *baseClient) Start(ctx context.Context, statsCh <-chan string) (status int) {
	dlog.Client.Trace("Starting base client")
	// Can be nil when serverless.
	if c.hostKeyCallback != nil {
		// Periodically check for unknown hosts, and ask the user whether to trust them or not.
		go c.hostKeyCallback.PromptAddHosts(ctx)
	}
	// Print client stats every time something on statsCh is received.
	go c.stats.Start(ctx, c.throttleCh, statsCh, c.Args.Quiet)

	var wg sync.WaitGroup
	wg.Add(len(c.connections))
	var mutex sync.Mutex

	for i, conn := range c.connections {
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

// startConnection manages the lifecycle of a single server connection,
// including retry logic for failed connections. This method runs in its
// own goroutine and handles connection establishment, operation execution,
// and automatic reconnection based on the retry configuration.
//
// Parameters:
//   ctx: Context for cancellation control
//   i: Index of this connection in the connections slice
//   conn: The connector managing communication with the specific server
//
// Returns:
//   int: Status code from the connection handler (0=success, >0=error)
//
// The connection lifecycle:
// 1. Starts the connector and waits for completion
// 2. Retrieves the final status from the connection handler
// 3. If retry is enabled and context allows, waits and reconnects
// 4. Continues until context cancellation or retry is disabled
func (c *baseClient) startConnection(ctx context.Context, i int,
	conn connectors.Connector) (status int) {

	for {
		connCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		conn.Start(connCtx, cancel, c.throttleCh, c.stats.connectionsEstCh)
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

		// Yes, we want to retry using reusable timer - PBO optimization
		if !retryTimer.Stop() {
			// Drain timer channel if it fired
			select {
			case <-retryTimer.C:
			default:
			}
		}
		retryTimer.Reset(constants.RetryTimerDuration)
		select {
		case <-retryTimer.C:
		case <-ctx.Done():
			return
		}
		dlog.Client.Debug(conn.Server(), "Reconnecting")
		conn = c.makeConnection(conn.Server(), c.sshAuthMethods, c.hostKeyCallback)
		c.connections[i] = conn
	}
}

// makeConnection creates a single connector for communicating with a specific server.
// The type of connector created depends on the Serverless configuration flag.
//
// Parameters:
//   server: Hostname and port of the target server
//   sshAuthMethods: SSH authentication methods to use for server connections
//   hostKeyCallback: Callback for handling SSH host key verification
//
// Returns:
//   connectors.Connector: Either a ServerConnection (SSH-based) or Serverless connector
//
// Connection types:
// - Serverless: Creates local file processing connector
// - Server mode: Creates SSH-based connector with authentication
func (c *baseClient) makeConnection(server string, sshAuthMethods []gossh.AuthMethod,
	hostKeyCallback client.HostKeyCallback) connectors.Connector {
	if c.Args.Serverless {
		return connectors.NewServerless(c.UserName, c.maker.makeHandler(server),
			c.maker.makeCommands())
	}
	return connectors.NewServerConnection(server, c.UserName, sshAuthMethods,
		hostKeyCallback, c.maker.makeHandler(server), c.maker.makeCommands())
}
