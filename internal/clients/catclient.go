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

// CatClient is a client for returning a whole file from the beginning to the end.
type CatClient struct {
	baseClient
}

// NewCatClient returns a new cat client.
func NewCatClient(args config.Args, cfg config.RuntimeConfig, loggers LoggerDependencies,
	colorizers ...*brush.Brush) (*CatClient, error) {
	if args.RegexStr != "" {
		return nil, errors.New("can't use regex with 'cat' operating mode")
	}
	args.Mode = omode.CatClient
	loggers = loggers.normalized()

	c := CatClient{
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
		return nil, fmt.Errorf("initialize cat client: %w", err)
	}
	return &c, nil
}

func (c CatClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server, c.clientLogger())
}

func (c CatClient) makeSessionSpec() SessionSpec {
	return NewSessionSpec(c.Args)
}
