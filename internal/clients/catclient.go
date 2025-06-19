package clients

import (
	"errors"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// CatClient is a client for returning a whole file from the beginning to the end.
type CatClient struct {
	CommonClient
}

// NewCatClient returns a new cat client.
func NewCatClient(args config.Args) (*CatClient, error) {
	if args.RegexStr != "" {
		return nil, errors.New("Can't use regex with 'cat' operating mode")
	}
	args.Mode = omode.CatClient

	c := CatClient{
		CommonClient: NewCommonClient(args, false),
	}

	c.init()
	c.makeConnections(c)
	return &c, nil
}
