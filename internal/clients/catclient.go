package clients

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// CatClient is a client for returning a whole file from the beginning to the end.
type CatClient struct {
	baseClient
}

// NewCatClient returns a new cat client.
func NewCatClient(args config.Args) (*CatClient, error) {
	if args.RegexStr != "" {
		return nil, errors.New("can't use regex with 'cat' operating mode")
	}
	args.Mode = omode.CatClient

	c := CatClient{
		baseClient: baseClient{
			mu:         newBaseClientMu(),
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      false,
			runtime:    newClientRuntimeBoundary(config.CurrentRuntime()),
		},
	}

	if err := c.initialize(c); err != nil {
		return nil, fmt.Errorf("initialize cat client: %w", err)
	}
	return &c, nil
}

func (c CatClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server)
}

func (c CatClient) makeSessionSpec() (SessionSpec, error) { //nolint:unparam // The sessionSpecMaker contract permits construction errors.
	return NewSessionSpec(c.Args), nil
}
