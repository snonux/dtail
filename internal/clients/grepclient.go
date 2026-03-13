package clients

import (
	"errors"
	"runtime"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
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

func (c GrepClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server)
}

func (c GrepClient) makeSessionSpec() (SessionSpec, error) {
	return NewSessionSpec(c.Args), nil
}

func (c GrepClient) makeCommands() (commands []string) {
	sessionSpec, err := c.makeSessionSpec()
	if err != nil {
		dlog.Client.FatalPanic("unable to build grep session spec", err)
	}
	commands, err = sessionSpec.Commands()
	if err != nil {
		dlog.Client.FatalPanic("unable to build grep commands from session spec", err)
	}
	return commands
}
