package clients

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/omode"
)

// CommonClient provides shared functionality for CatClient, GrepClient, and TailClient
type CommonClient struct {
	baseClient
}

// NewCommonClient creates a new common client with the specified configuration
func NewCommonClient(args config.Args, retry bool) CommonClient {
	return CommonClient{
		baseClient: baseClient{
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      retry,
		},
	}
}

// makeHandler returns a standard client handler
func (c CommonClient) makeHandler(server string) handlers.Handler {
	return handlers.NewClientHandler(server)
}

// makeCommands generates commands based on the client mode
func (c CommonClient) makeCommands() (commands []string) {
	regex, err := c.Regex.Serialize()
	if err != nil {
		dlog.Client.FatalPanic(err)
	}
	for _, file := range strings.Split(c.What, ",") {
		commands = append(commands, fmt.Sprintf("%s:%s %s %s",
			c.Mode.String(), c.Args.SerializeOptions(), file, regex))
	}
	if c.Mode == omode.TailClient {
		dlog.Client.Debug(commands)
	}
	return
}