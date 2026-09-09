package clients

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// GrepClient searches a remote file for all lines matching a regular
// expression. Only the matching lines are displayed.
type GrepClient struct {
	baseClient
}

// NewGrepClient creates a new grep client.
func NewGrepClient(args config.Args) (*GrepClient, error) {
	if args.RegexStr == "" {
		return nil, errors.New("No regex specified, use '-regex' flag")
	}
	args.Mode = omode.GrepClient

	c := GrepClient{
		baseClient: baseClient{
			mu:         newBaseClientMu(),
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      false,
			runtime:    newClientRuntimeBoundary(config.CurrentRuntime()),
		},
	}

	if err := c.initialize(c); err != nil {
		return nil, fmt.Errorf("initialize grep client: %w", err)
	}
	return &c, nil
}

func (c GrepClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server)
}

func (c GrepClient) makeSessionSpec() (SessionSpec, error) {
	return NewSessionSpec(c.Args), nil
}
