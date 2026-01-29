package handlers

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	user "github.com/mimecast/dtail/internal/user/server"
)

// ServerHandler implements the Reader and Writer interfaces to handle
// the Bi-directional communication between SSH client and server.
// This handler implements the handler of the SSH server.
type ServerHandler struct {
	baseHandler
	catLimiter  chan struct{}
	tailLimiter chan struct{}
	regex       string
	// Track pending files waiting for limiter slots
	pendingFiles int32
}

var _ Handler = (*ServerHandler)(nil)

// NewServerHandler returns the server handler.
func NewServerHandler(user *user.User, catLimiter,
	tailLimiter chan struct{}) *ServerHandler {

	dlog.Server.Debug(user, "Creating new server handler")
	h := ServerHandler{
		baseHandler: baseHandler{
			done:             internal.NewDone(),
			lines:            make(chan *line.Line, 100),
			serverMessages:   make(chan string, 10),
			maprMessages:     make(chan string, 10),
			ackCloseReceived: make(chan struct{}),
			user:             user,
		},
		catLimiter:  catLimiter,
		tailLimiter: tailLimiter,
		regex:       ".",
	}
	h.handleCommandCb = h.handleUserCommand

	fqdn, err := config.Hostname()
	if err != nil {
		dlog.Server.FatalPanic(err)
	}

	s := strings.Split(fqdn, ".")
	h.hostname = s[0]

	return &h
}

func (h *ServerHandler) handleUserCommand(ctx context.Context, ltx lcontext.LContext,
	argc int, args []string, commandName string) {

	dlog.Server.Debug(h.user, "Handling user command", argc, args)
	h.incrementActiveCommands()
	commandFinished := func() {
		activeCommands := h.decrementActiveCommands()
		pendingFiles := atomic.LoadInt32(&h.pendingFiles)
		dlog.Server.Debug(h.user, "Command finished", "activeCommands", activeCommands, "pendingFiles", pendingFiles)
		
		// Only shutdown if no active commands AND no pending files
		if activeCommands == 0 && pendingFiles == 0 {
			h.shutdown()
		}
	}

	switch commandName {
	case "grep":
		command := newReadCommand(h, omode.GrepClient)
		go func() {
			command.Start(ctx, ltx, argc, args, 1)
			commandFinished()
		}()
	case "cat":
		command := newReadCommand(h, omode.CatClient)
		go func() {
			command.Start(ctx, ltx, argc, args, 1)
			commandFinished()
		}()
	case "tail":
		command := newReadCommand(h, omode.TailClient)
		go func() {
			command.Start(ctx, ltx, argc, args, 10)
			commandFinished()
		}()
	case "map":
		command, aggregate, turboAggregate, err := newMapCommand(h, argc, args)
		if err != nil {
			h.sendln(h.serverMessages, err.Error())
			dlog.Server.Error(h.user, err)
			commandFinished()
			return
		}
		h.aggregate = aggregate
		h.turboAggregate = turboAggregate
		go func() {
			command.Start(ctx, h.maprMessages)
			commandFinished()
		}()
	case ".ack":
		h.handleAckCommand(argc, args)
		commandFinished()
	default:
		h.sendln(h.serverMessages, dlog.Server.Error(h.user,
			"Received unknown user command", commandName, argc, args))
		commandFinished()
	}
}
