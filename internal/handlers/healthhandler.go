package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/logging"
	user "github.com/mimecast/dtail/internal/sessionuser"
)

// HealthHandler is for the remote health check.
type HealthHandler struct {
	*baseHandler
}

// NewHealthHandler returns the server handler.
func NewHealthHandler(user *user.User, serverCfg *config.ServerConfig, logger logging.Logger) (*HealthHandler, error) {
	logger = logging.OrNop(logger)
	logger.Debug(user, "Creating new server health handler")
	if user == nil {
		return nil, fmt.Errorf("create health handler: user must not be nil")
	}

	// A nil configuration uses the protocol-safe default for isolated health checks.
	maxFrameSize := config.DefaultMaxCommandFrameSize
	if serverCfg != nil && serverCfg.MaxCommandFrameSize > 0 {
		maxFrameSize = serverCfg.MaxCommandFrameSize
	}

	h := HealthHandler{
		baseHandler: newBaseHandler(baseHandlerConfig{
			logger:              logger,
			user:                user,
			maxCommandFrameSize: maxFrameSize,
		}),
	}
	h.handleCommandCb = h.handleHealthCommand

	fqdn, err := handlerHostname()
	if err != nil {
		return nil, fmt.Errorf("create health handler: resolve hostname: %w", err)
	}
	s := strings.Split(fqdn, ".")
	h.hostname = s[0]
	return &h, nil
}

func (h *HealthHandler) handleHealthCommand(ctx context.Context,
	ltx lcontext.LContext, argc int, args []string, commandName string) {

	h.Logger().Debug(h.user, "Handling health command", argc, args)
	switch commandName {
	case "health":
		h.send(h.serverMessages, "OK")
	case ".ack":
		h.handleAckCommand(argc, args)
	default:
		h.send(h.serverMessages, h.Logger().Error(h.user,
			"Received unknown health command", commandName, argc, args))
	}
	// Release the per-command cancel before shutdown so the watcher
	// goroutine spawned by newCommandContext exits via <-ctx.Done() and
	// not only via the <-h.done.Done() safety net.
	cancelCommandContext(ctx)
	h.shutdown()
}
