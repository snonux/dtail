package clients

import (
	"runtime"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/omode"
)

// TailClient is used for tailing remote log files (opening, seeking to the end and returning only new incoming lines).
type TailClient struct {
	baseClient
}

// NewTailClient returns a new TailClient.
func NewTailClient(args config.Args) (*TailClient, error) {
	args.Mode = omode.TailClient
	c := TailClient{
		baseClient: baseClient{
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      true,
			runtime:    newClientRuntimeBoundary(config.CurrentRuntime()),
		},
	}

	c.init()
	if err := c.makeConnections(c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c TailClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server)
}

func (c TailClient) makeSessionSpec() (SessionSpec, error) {
	return NewSessionSpec(c.Args), nil
}

func (c TailClient) makeCommands() (commands []string) {
	sessionSpec, err := c.makeSessionSpec()
	if err != nil {
		dlog.Client.FatalPanic("unable to build tail session spec", err)
	}
	commands, err = sessionSpec.Commands()
	if err != nil {
		dlog.Client.FatalPanic("unable to build tail commands from session spec", err)
	}
	return commands
}
