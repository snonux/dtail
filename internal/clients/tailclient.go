package clients

import (
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// TailClient is used for tailing remote log files (opening, seeking to the end and returning only new incoming lines).
type TailClient struct {
	CommonClient
}

// NewTailClient returns a new TailClient.
func NewTailClient(args config.Args) (*TailClient, error) {
	args.Mode = omode.TailClient
	c := TailClient{
		CommonClient: NewCommonClient(args, true),
	}

	c.init()
	c.makeConnections(c)
	return &c, nil
}
