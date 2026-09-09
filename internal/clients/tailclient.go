package clients

import (
	"fmt"
	"runtime"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
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
			mu:         newBaseClientMu(),
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      true,
			runtime:    newClientRuntimeBoundary(config.CurrentRuntime()),
		},
	}

	if err := c.initialize(c); err != nil {
		return nil, fmt.Errorf("initialize tail client: %w", err)
	}
	return &c, nil
}

func (c TailClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server)
}

func (c TailClient) makeSessionSpec() (SessionSpec, error) {
	return NewSessionSpec(c.Args), nil
}
