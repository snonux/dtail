package handlers

import (
	"context"
	"fmt"
	"io"

	"github.com/mimecast/dtail/internal/authkey"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/fs/readhub"
	"github.com/mimecast/dtail/internal/logging"
	user "github.com/mimecast/dtail/internal/sessionuser"
)

// Dependencies contains the process-owned resources used by a session handler.
type Dependencies struct {
	ServerConfig *config.ServerConfig
	CatLimiter   chan struct{}
	TailLimiter  chan struct{}
	// ReadHub shares follow reads of the same file between sessions; nil
	// makes every session read privately (see NewReadHub).
	ReadHub          *readhub.Hub
	AuthKeyStore     *authkey.Store
	ServerlessOutput io.Writer
	Loggers          HandlerLoggers
	Capabilities     []string
	Colorizer        Colorizer
	Hostname         string
}

// NewForUser creates the handler appropriate for the authenticated user.
func NewForUser(ctx context.Context, user *user.User, dependencies Dependencies) (Handler, error) {
	if ctx == nil {
		return nil, fmt.Errorf("create handler: context must not be nil")
	}
	if user != nil && user.Name == config.HealthUser {
		maxFrameSize := config.DefaultMaxCommandFrameSize
		if dependencies.ServerConfig != nil && dependencies.ServerConfig.MaxCommandFrameSize > 0 {
			maxFrameSize = dependencies.ServerConfig.MaxCommandFrameSize
		}
		return NewHealthHandler(ctx, user, maxFrameSize, dependencies.Hostname,
			dependencies.Loggers.Diagnostics)
	}
	return NewServerHandler(ctx, user, dependencies)
}

// NewReadHub returns the hub that lets dserver sessions tailing the same file
// share one reader of it, configured like a session's private reader, or nil
// when the configuration turns shared reads off.
func NewReadHub(serverCfg *config.ServerConfig, logger logging.Logger) *readhub.Hub {
	if serverCfg == nil || serverCfg.SharedReadsDisable {
		return nil
	}
	timings := newReadTimings(serverCfg)
	return readhub.New(readhub.Options{
		Logger:        logger,
		MaxLineLength: timings.maxLineLength,
		RetryInterval: timings.readRetryInterval,
	})
}
