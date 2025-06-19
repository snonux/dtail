package clients

import (
	"context"
	"fmt"
	"runtime"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"

	gossh "golang.org/x/crypto/ssh"
)

// HealthClient provides distributed health checking functionality for DTail
// servers. It performs basic connectivity and operational status checks across
// multiple servers simultaneously, providing a quick way to verify system health.
//
// Key features:
// - Simultaneous health checks across multiple servers
// - Special health user authentication for minimal privileges
// - Simple pass/fail status reporting with detailed messages
// - Support for both server and serverless mode health checks
// - No retry logic (immediate health assessment)
//
// HealthClient directly embeds baseClient for core functionality and implements
// specialized health check commands and result interpretation.
type HealthClient struct {
	baseClient
}

// NewHealthClient creates a new HealthClient configured for distributed health checking.
// This constructor sets up special authentication and configuration for health check
// operations with minimal server privileges.
//
// Parameters:
//   args: Complete configuration arguments including servers and connection options
//
// Returns:
//   *HealthClient: Configured client ready to perform health checks
//   error: Configuration or initialization error, if any
//
// Special configuration:
// - Uses the dedicated health user account with password authentication
// - Sets operating mode to HealthClient
// - Disables connection retry for immediate health assessment
// - Configures connection throttling based on CPU cores
//
// The returned client is fully initialized and ready to call Start().
func NewHealthClient(args config.Args) (*HealthClient, error) {
	args.Mode = omode.HealthClient
	args.UserName = config.HealthUser
	args.SSHAuthMethods = append(args.SSHAuthMethods, gossh.Password(config.HealthUser))

	c := HealthClient{
		baseClient: baseClient{
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      false,
		},
	}

	c.init()
	c.makeConnections(c)
	return &c, nil
}

// makeHandler creates a health-specific handler for performing health checks
// on the specified server. This method implements the maker interface requirement
// and provides the handler used for health check operations.
//
// Parameters:
//   server: The server hostname/address for this handler
//
// Returns:
//   handlers.Handler: A HealthHandler configured for the specified server
//
// The returned handler manages health check protocol communication and
// provides simple pass/fail status reporting for server health assessment.
func (c HealthClient) makeHandler(server string) handlers.Handler {
	return handlers.NewHealthHandler(server)
}

// makeCommands generates the health check command for DTail servers.
// This method implements the maker interface requirement and creates
// the simple "health" command for server health verification.
//
// Returns:
//   []string: List containing a single "health" command
//
// The health command is a simple protocol command that instructs
// the DTail server to perform basic operational checks and return
// a status indicating whether the server is functioning properly.
func (c HealthClient) makeCommands() (commands []string) {
	commands = append(commands, "health")
	return
}

// Start performs health checks across all configured servers and provides
// detailed status reporting. This method coordinates health check execution
// and interprets the results with user-friendly status messages.
//
// Parameters:
//   ctx: Context for cancellation and timeout control
//   statsCh: Channel for receiving statistics display requests
//
// Returns:
//   int: Health status code (0=healthy, 1=warning, 2=critical, other=unknown)
//
// Status interpretation:
// - 0: All servers are healthy and operating normally
// - 1: Warning condition (e.g., serverless mode limitations)
// - 2: Critical condition (servers not operating properly)
// - Other: Unknown status code received from servers
//
// The method provides detailed output messages explaining the health status
// and any recommendations for addressing issues.
func (c *HealthClient) Start(ctx context.Context, statsCh <-chan string) int {
	status := c.baseClient.Start(ctx, statsCh)

	switch status {
	case 0:
		if c.Serverless {
			fmt.Printf("WARNING: All seems fine but the check only run in serverless mode" +
				", please specify a remote server via --server hostname:port\n")
			return 1
		}
		fmt.Printf("OK: All fine at %s :-)\n", c.ServersStr)
	case 2:
		if c.Serverless {
			fmt.Printf("CRITICAL: DTail server not operating properly (using " +
				"serverless connction)!\n")
			return 2
		}
		fmt.Printf("CRITICAL: DTail server not operating properly at %s!\n",
			c.ServersStr)
	default:
		if c.Serverless {
			fmt.Printf("UNKNOWN: Received unknown status code %d (using serverless "+
				"connection)\n", status)
			return status
		}
		fmt.Printf("UNKNOWN: Received unknown status code %d from %s!\n",
			status, c.ServersStr)
	}

	return status
}
