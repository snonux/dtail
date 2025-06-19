package clients

import (
	"errors"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// GrepClient provides distributed text searching functionality, scanning
// files across multiple servers simultaneously for lines matching a regular
// expression pattern. Only lines that match the specified pattern are returned,
// making it ideal for log analysis and content filtering.
//
// Key features:
// - Distributed regex-based line searching across multiple servers
// - Support for both normal and inverted pattern matching
// - Efficient streaming of matching lines only
// - Built-in regex validation and compilation
// - Immediate termination after scanning (no continuous monitoring)
//
// GrepClient embeds CommonClient to inherit standard connection management,
// SSH authentication, and command generation capabilities.
type GrepClient struct {
	CommonClient
}

// NewGrepClient creates a new GrepClient configured for distributed text searching.
// This constructor validates that a regex pattern is provided and sets up the
// client for one-time file scanning operations.
//
// Parameters:
//   args: Complete configuration arguments including servers, files, regex pattern, and options
//
// Returns:
//   *GrepClient: Configured client ready to start text searching operations
//   error: Configuration error if no regex pattern is specified
//
// Configuration requirements:
// - Requires a valid regex pattern via the RegexStr field
// - Sets operating mode to GrepClient
// - Disables automatic connection retry (one-time operation)
// - Initializes regex compilation and server connections
//
// The returned client is fully initialized and ready to call Start().
func NewGrepClient(args config.Args) (*GrepClient, error) {
	if args.RegexStr == "" {
		return nil, errors.New("No regex specified, use '-regex' flag")
	}
	args.Mode = omode.GrepClient

	c := GrepClient{
		CommonClient: NewCommonClient(args, false),
	}

	c.init()
	c.makeConnections(c)
	return &c, nil
}
