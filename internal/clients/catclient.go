package clients

import (
	"errors"
	"runtime"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/omode"
)

// CatClient is a client for returning a whole file from the beginning to the end.
type CatClient struct {
	baseClient
}

// NewCatClient returns a new cat client.
func NewCatClient(args config.Args) (*CatClient, error) {
	if args.RegexStr != "" {
		return nil, errors.New("Can't use regex with 'cat' operating mode")
	}
	args.Mode = omode.CatClient

	c := CatClient{
		baseClient: baseClient{
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      false,
			runtime:    newClientRuntimeBoundary(config.CurrentRuntime()),
		},
	}

	c.init()
	c.makeConnections(c)
	return &c, nil
}

func (c CatClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server)
}

func (c CatClient) makeSessionSpec() (SessionSpec, error) {
	return NewSessionSpec(c.Args), nil
}

func (c CatClient) makeCommands() (commands []string) {
	sessionSpec, err := c.makeSessionSpec()
	if err != nil {
		dlog.Client.FatalPanic("unable to build cat session spec", err)
	}
	commands, err = sessionSpec.Commands()
	if err != nil {
		dlog.Client.FatalPanic("unable to build cat commands from session spec", err)
	}
	return commands
}
