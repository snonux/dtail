package clients

import (
	"errors"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// CatClient provides distributed file reading functionality, retrieving complete
// file contents from beginning to end across multiple servers simultaneously.
// Unlike TailClient which monitors for new content, CatClient reads existing
// file contents and terminates when complete.
//
// Key features:
// - Simultaneous reading of files across multiple servers
// - Complete file content retrieval from start to finish
// - No regex filtering support (files are read in their entirety)
// - Immediate termination after reading (no continuous monitoring)
// - Efficient handling of large files through streaming
//
// CatClient embeds CommonClient to inherit standard connection management,
// SSH authentication, and command generation capabilities.
type CatClient struct {
	CommonClient
}

// NewCatClient creates a new CatClient configured for distributed file reading.
// This constructor validates the configuration and sets up the client for
// one-time file content retrieval operations.
//
// Parameters:
//   args: Complete configuration arguments including servers, files, and options
//
// Returns:
//   *CatClient: Configured client ready to start file reading operations
//   error: Configuration error if regex is specified (not supported for cat operations)
//
// Configuration validation:
// - Ensures no regex pattern is specified (cat reads entire files)
// - Sets operating mode to CatClient
// - Disables automatic connection retry (one-time operation)
// - Initializes connections to all discovered servers
//
// The returned client is fully initialized and ready to call Start().
func NewCatClient(args config.Args) (*CatClient, error) {
	if args.RegexStr != "" {
		return nil, errors.New("Can't use regex with 'cat' operating mode")
	}
	args.Mode = omode.CatClient

	c := CatClient{
		CommonClient: NewCommonClient(args, false),
	}

	c.init()
	c.makeConnections(c)
	return &c, nil
}
