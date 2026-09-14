package clients

import (
	"fmt"
	"runtime"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// TailClient is used for tailing remote log files (opening, seeking to the end and returning only new incoming lines).
type TailClient struct {
	baseClient
}

// NewTailClient returns a new TailClient.
func NewTailClient(args config.Args, loggers LoggerDependencies, colorizers ...*brush.Brush) (*TailClient, error) {
	args.Mode = omode.TailClient
	loggers = loggers.normalized()
	c := TailClient{
		baseClient: baseClient{
			mu:         newBaseClientMu(),
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.GOMAXPROCS(0)),
			retry:      true,
			loggers:    loggers,
			colorizer:  firstColorizer(colorizers),
		},
	}

	if err := c.initialize(c); err != nil {
		return nil, fmt.Errorf("initialize tail client: %w", err)
	}
	return &c, nil
}

func (c TailClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server, c.clientLogger())
}

func (c TailClient) makeSessionSpec() SessionSpec {
	return NewSessionSpec(c.Args)
}
