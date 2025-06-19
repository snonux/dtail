package clients

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/omode"
)

// CommonClient provides shared functionality for CatClient, GrepClient, and TailClient.
// It embeds baseClient to inherit core connection management and SSH functionality,
// while providing specialized command generation and handler creation for standard
// file operations (cat, grep, tail).
//
// This structure reduces code duplication across the three most common client types
// by centralizing their shared behavior and configuration patterns.
type CommonClient struct {
	baseClient
}

// NewCommonClient creates a new CommonClient with the specified configuration.
// This constructor initializes the embedded baseClient with appropriate settings
// for standard file operations.
//
// Parameters:
//   args: Complete configuration arguments for the client
//   retry: Whether to automatically retry failed connections
//
// Returns:
//   CommonClient: Configured client ready for initialization and connection setup
//
// The client is configured with:
// - Connection throttling based on CPU cores
// - Retry behavior as specified
// - All provided configuration arguments
func NewCommonClient(args config.Args, retry bool) CommonClient {
	return CommonClient{
		baseClient: baseClient{
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      retry,
		},
	}
}

// makeHandler creates a standard client handler for basic file operations.
// This method implements the maker interface requirement and provides the
// handler used for cat, grep, and tail operations.
//
// Parameters:
//   server: The server hostname/address for this handler
//
// Returns:
//   handlers.Handler: A ClientHandler configured for the specified server
//
// The returned handler manages the protocol communication and result processing
// for standard file operations across all CommonClient-based client types.
func (c CommonClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server)
}

// makeCommands generates the appropriate DTail server commands based on the
// client's operating mode and configuration. This method implements the maker
// interface requirement and creates commands for cat, grep, or tail operations.
//
// Returns:
//   []string: List of commands to send to DTail servers
//
// Command generation process:
// 1. Serializes the regex pattern for transmission
// 2. Creates one command per file specified in the What field
// 3. Includes mode-specific options and parameters
// 4. Formats commands using the mode:options filename regex pattern
//
// The generated commands follow the DTail protocol format and include
// all necessary options for proper server-side execution.
func (c CommonClient) makeCommands() (commands []string) {
	regex, err := c.Regex.Serialize()
	if err != nil {
		dlog.Client.FatalPanic(err)
	}
	for _, file := range strings.Split(c.What, ",") {
		commands = append(commands, fmt.Sprintf("%s:%s %s %s",
			c.Mode.String(), c.Args.SerializeOptions(), file, regex))
	}
	if c.Mode == omode.TailClient {
		dlog.Client.Debug(commands)
	}
	return
}