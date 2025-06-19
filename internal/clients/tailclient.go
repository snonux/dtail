package clients

import (
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// TailClient provides distributed log file tailing functionality, continuously
// monitoring remote log files and streaming new content as it appears. Unlike
// traditional tail implementations, TailClient can monitor multiple files across
// multiple servers simultaneously, aggregating the results in real-time.
//
// Key features:
// - Continuous monitoring of log files across multiple servers
// - Real-time streaming of new content only (seeks to end of files)
// - Automatic connection retry for long-running operations
// - Regex-based line filtering with normal and inverted matching
// - Graceful handling of log rotation and file recreation
//
// TailClient embeds CommonClient to inherit standard connection management,
// SSH authentication, and command generation capabilities.
type TailClient struct {
	CommonClient
}

// NewTailClient creates a new TailClient configured for distributed log tailing.
// This constructor sets up the client for continuous monitoring operations with
// automatic retry enabled for long-running tail operations.
//
// Parameters:
//   args: Complete configuration arguments including servers, files, and options
//
// Returns:
//   *TailClient: Configured client ready to start tailing operations
//   error: Configuration or initialization error, if any
//
// Configuration:
// - Sets operating mode to TailClient
// - Enables automatic connection retry for continuous operation
// - Initializes regex compilation and SSH authentication
// - Establishes connections to all discovered servers
//
// The returned client is fully initialized and ready to call Start().
func NewTailClient(args config.Args) (*TailClient, error) {
	args.Mode = omode.TailClient
	c := TailClient{
		CommonClient: NewCommonClient(args, true),
	}

	c.init()
	c.makeConnections(c)
	return &c, nil
}
