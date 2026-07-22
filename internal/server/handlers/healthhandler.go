package handlers

import (
	"context"
	"strings"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	user "github.com/mimecast/dtail/internal/user/server"
)

// HealthHandler is for the remote health check.
type HealthHandler struct {
	baseHandler
}

// NewHealthHandler returns the server handler.
func NewHealthHandler(user *user.User) *HealthHandler {
	dlog.Server.Debug(user, "Creating new server health handler")

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
			done:                internal.NewDone(),
			lines:               make(chan *line.Line, 100),
			serverMessages:      make(chan string, 10),
			maprMessages:        make(chan string, 10),
			ackCloseReceived:    make(chan struct{}),
			user:                user,
			codec:               newProtocolCodec(user),
			maxCommandFrameSize: maxFrameSize,
		},
	}
	h.handleCommandCb = h.handleHealthCommand

	fqdn, err := config.Hostname()
	if err != nil {
		dlog.Server.FatalPanic(err)
	}
	s := strings.Split(fqdn, ".")
	h.hostname = s[0]
	return &h
}

func (h *HealthHandler) handleHealthCommand(ctx context.Context,
	ltx lcontext.LContext, argc int, args []string, commandName string) {

	dlog.Server.Debug(h.user, "Handling health command", argc, args)
	switch commandName {
	case "health":
		h.send(h.serverMessages, "OK")
	case ".ack":
		h.handleAckCommand(argc, args)
	default:
		h.send(h.serverMessages, dlog.Server.Error(h.user,
			"Received unknown health command", commandName, argc, args))
	}
	// Release the per-command cancel before shutdown so the watcher
	// goroutine spawned by newCommandContext exits via <-ctx.Done() and
	// not only via the <-h.done.Done() safety net.
	cancelCommandContext(ctx)
	h.shutdown()
}
