package clients

import (
	"errors"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// GrepClient searches a remote file for all lines matching a regular
// expression. Only the matching lines are displayed.
type GrepClient struct {
	CommonClient
}

// NewGrepClient creates a new grep client.
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
