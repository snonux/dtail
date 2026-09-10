package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/logging"
	user "github.com/mimecast/dtail/internal/user/server"
)

// HealthHandler is for the remote health check.
type HealthHandler struct {
	baseHandler
}

// NewHealthHandler returns the server handler.
func NewHealthHandler(user *user.User, logger logging.Logger) (*HealthHandler, error) {
	logger = logging.OrNop(logger)
	logger.Debug(user, "Creating new server health handler")
	if user == nil {
		return nil, fmt.Errorf("create health handler: user must not be nil")
	}

	// Read the frame-size limit from the global server config when available.
	// The global config may be nil in tests that exercise the health handler in
	// isolation; the fallback keeps those tests working while still enforcing
	// the limit in production.
	maxFrameSize := config.DefaultMaxCommandFrameSize
	if config.Server != nil && config.Server.MaxCommandFrameSize > 0 {
		maxFrameSize = config.Server.MaxCommandFrameSize
	}

	h := HealthHandler{
		baseHandler: baseHandler{
			logger:              logger,
			done:                internal.NewDone(),
			lines:               make(chan *line.Line, 100),
			serverMessages:      make(chan string, 10),
			maprMessages:        make(chan string, 10),
			ackCloseReceived:    make(chan struct{}),
			user:                user,
			codec:               newProtocolCodec(user, logger),
			maxCommandFrameSize: maxFrameSize,
		},
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
