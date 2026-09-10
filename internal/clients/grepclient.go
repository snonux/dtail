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
func NewGrepClient(args config.Args, loggers LoggerDependencies) (*GrepClient, error) {
	if args.RegexStr == "" {
		return nil, errors.New("no regex specified, use '-regex' flag")
	}
	args.Mode = omode.GrepClient
	loggers = loggers.normalized()

	c := GrepClient{
		baseClient: baseClient{
			mu:         newBaseClientMu(),
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      false,
			loggers:    loggers,
		},
	}

	if err := c.initialize(c); err != nil {
		return nil, fmt.Errorf("initialize grep client: %w", err)
	}
	return &c, nil
}

func (c GrepClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server, c.clientLogger())
}

func (c GrepClient) makeSessionSpec() (SessionSpec, error) { //nolint:unparam // The sessionSpecMaker contract permits construction errors.
	return NewSessionSpec(c.Args), nil
}
