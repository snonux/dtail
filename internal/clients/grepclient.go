package clients

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// GrepClient searches a remote file for all lines matching a regular
// expression. Only the matching lines are displayed.
type GrepClient struct {
	baseClient
}

// NewGrepClient creates a new grep client.
func NewGrepClient(args config.Args, cfg config.RuntimeConfig, loggers LoggerDependencies,
	colorizers ...*brush.Brush) (*GrepClient, error) {
	if args.RegexStr == "" {
		return nil, errors.New("no regex specified, use '-regex' flag")
	}
	args.Mode = omode.GrepClient
	loggers = loggers.normalized()

	c := GrepClient{
		baseClient: baseClient{
			mu:         newBaseClientMu(),
			Args:       args,
			cfg:        cfg,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.GOMAXPROCS(0)),
			retry:      false,
			loggers:    loggers,
			colorizer:  firstColorizer(colorizers),
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

func (c GrepClient) makeSessionSpec() SessionSpec {
	return NewSessionSpec(c.Args)
}
