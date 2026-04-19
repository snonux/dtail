package handlers

import (
	"context"
	"encoding/base64"
	"strings"
	"sync/atomic"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	sshserver "github.com/mimecast/dtail/internal/ssh/server"
	user "github.com/mimecast/dtail/internal/user/server"

	gossh "golang.org/x/crypto/ssh"
)

// ServerHandler implements the Reader and Writer interfaces to handle
// the Bi-directional communication between SSH client and server.
// This handler implements the handler of the SSH server.
type ServerHandler struct {
	baseHandler
	catLimiter   chan struct{}
	tailLimiter  chan struct{}
	serverCfg    *config.ServerConfig
	authKeyStore *sshserver.AuthKeyStore
	regex        string
	commands     map[string]commandHandler
	sessionState sessionCommandState
	// Track pending files waiting for limiter slots
	pendingFiles int32
}

type commandHandler func(context.Context, lcontext.LContext, int, []string, func())

var _ Handler = (*ServerHandler)(nil)

// NewServerHandler returns the server handler.
func NewServerHandler(user *user.User, catLimiter,
	tailLimiter chan struct{}, serverCfg *config.ServerConfig,
	authKeyStore *sshserver.AuthKeyStore) *ServerHandler {

	dlog.Server.Debug(user, "Creating new server handler")
	if serverCfg == nil {
		dlog.Server.FatalPanic("Missing server config in NewServerHandler")
	}

	h := ServerHandler{
		baseHandler: baseHandler{
			done:             internal.NewDone(),
			lines:            make(chan *line.Line, 100),
			serverMessages:   make(chan string, 10),
			maprMessages:     make(chan string, 10),
			ackCloseReceived: make(chan struct{}),
			user:             user,
			codec:            newProtocolCodec(user),
		},
		catLimiter:   catLimiter,
		tailLimiter:  tailLimiter,
		serverCfg:    serverCfg,
		authKeyStore: authKeyStore,
		regex:        ".",
	}
	if h.authKeyStore == nil {
		h.authKeyStore = sshserver.AuthKeys()
	}
	h.handleCommandCb = h.handleUserCommand
	h.commands = h.newCommandRegistry()
	h.turbo.configure(h.turboManagerConfig())
	h.baseHandler.activeGeneration = h.sessionState.currentGeneration

	fqdn, err := config.Hostname()
	if err != nil {
		dlog.Server.FatalPanic(err)
	}

	s := strings.Split(fqdn, ".")
	h.hostname = s[0]
	h.send(h.serverMessages, protocol.HiddenCapabilitiesPrefix+protocol.CapabilityQueryUpdateV1)

	return &h
}

func (h *ServerHandler) handleUserCommand(ctx context.Context, ltx lcontext.LContext,
	argc int, args []string, commandName string) {

	dlog.Server.Debug(h.user, "Handling user command", argc, args)
	shutdownOnCompletion := shouldShutdownOnCommandCompletion(commandName)
	h.incrementActiveCommands()
	commandFinished := func() {
		activeCommands := h.decrementActiveCommands()
		pendingFiles := atomic.LoadInt32(&h.pendingFiles)
		dlog.Server.Debug(h.user, "Command finished", "activeCommands", activeCommands, "pendingFiles", pendingFiles)

		// Release the per-command context + watcher goroutine created for
		// this invocation (see baseHandler.handleCommand). In the session
		// dispatch path ctx carries no command cancel and this is a no-op;
		// the session state owns cancellation there.
		cancelCommandContext(ctx)

		// Only shutdown if no active commands AND no pending files.
		// AUTHKEY is a session-side effect command and should not terminate the shell
		// because user commands may still follow in the same session.
		if shutdownOnCompletion && activeCommands == 0 && pendingFiles == 0 && !h.sessionState.keepAlive() {
			h.shutdown()
		}
	}

	handler, found := h.commands[commandName]
	if !found {
		h.sendln(h.serverMessages, dlog.Server.Error(h.user,
			"Received unknown user command", commandName, argc, args))
		commandFinished()
		return
	}

	handler(ctx, ltx, argc, args, commandFinished)
}

func shouldShutdownOnCommandCompletion(commandName string) bool {
	switch {
	case strings.EqualFold(commandName, "AUTHKEY"):
		return false
	case strings.EqualFold(commandName, "SESSION"):
		return false
	default:
		return true
	}
}

func (h *ServerHandler) newCommandRegistry() map[string]commandHandler {
	return map[string]commandHandler{
		"grep":    h.makeReadCommandHandler(omode.GrepClient, 1),
		"cat":     h.makeReadCommandHandler(omode.CatClient, 1),
		"tail":    h.makeReadCommandHandler(omode.TailClient, 10),
		"map":     h.handleMapCommand,
		".ack":    h.handleAckUserCommand,
		"AUTHKEY": h.handleAuthKeyCommand,
		"SESSION": h.handleSessionCommand,
		"authkey": h.handleAuthKeyCommand,
		"session": h.handleSessionCommand,
	}
}

func (h *ServerHandler) makeReadCommandHandler(mode omode.Mode, tailBackoff int) commandHandler {
	return func(ctx context.Context, ltx lcontext.LContext, argc int, args []string, commandFinished func()) {
		command := newReadCommand(h, mode)
		go func() {
			command.Start(ctx, ltx, argc, args, tailBackoff)
			commandFinished()
		}()
	}
}

func (h *ServerHandler) handleMapCommand(ctx context.Context, _ lcontext.LContext, argc int, args []string, commandFinished func()) {
	command, aggregate, turboAggregate, err := newMapCommand(h, argc, args)
	if err != nil {
		h.sendln(h.serverMessages, err.Error())
		dlog.Server.Error(h.user, err)
		commandFinished()
		return
	}

	// Use atomic setters so concurrent reads from Shutdown, HasRegularAggregate,
	// TurboAggregate, and resetSessionAggregates are race-free.
	h.setAggregate(aggregate)
	h.setTurboAggregate(turboAggregate)
	maprMessages, closeMaprMessages := h.newGeneratedMaprMessagesChannel(ctx, sessionGenerationFromContext(ctx))
	go func() {
		command.Start(ctx, maprMessages)
		closeMaprMessages()
		commandFinished()
	}()
}

func (h *ServerHandler) handleAckUserCommand(_ context.Context, _ lcontext.LContext, argc int, args []string, commandFinished func()) {
	h.handleAckCommand(argc, args)
	commandFinished()
}

func (h *ServerHandler) handleAuthKeyCommand(_ context.Context, _ lcontext.LContext,
	argc int, args []string, commandFinished func()) {

	defer commandFinished()

	if !h.serverCfg.AuthKeyEnabled {
		h.sendln(h.serverMessages, "AUTHKEY ERR feature disabled")
		return
	}

	if argc < 2 || strings.TrimSpace(args[1]) == "" {
		h.sendln(h.serverMessages, "AUTHKEY ERR missing public key")
		return
	}

	decodedPubKey, err := base64.StdEncoding.DecodeString(args[1])
	if err != nil {
		h.sendln(h.serverMessages, "AUTHKEY ERR invalid base64")
		return
	}

	pubKey, err := gossh.ParsePublicKey(decodedPubKey)
	if err != nil {
		h.sendln(h.serverMessages, "AUTHKEY ERR invalid public key")
		return
	}

	if h.authKeyStore == nil {
		h.sendln(h.serverMessages, "AUTHKEY ERR internal key store unavailable")
		return
	}
	h.authKeyStore.Add(h.user.Name, pubKey)
	h.sendln(h.serverMessages, "AUTHKEY OK")
}

func (h *ServerHandler) newGeneratedMaprMessagesChannel(ctx context.Context, generation uint64) (chan string, func()) {
	maprMessages := make(chan string, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case message, ok := <-maprMessages:
				if !ok {
					return
				}
				h.send(h.maprMessages, encodeGeneratedMessage(generation, message))
			case <-ctx.Done():
				return
			case <-h.done.Done():
				return
			}
		}
	}()
	return maprMessages, func() {
		close(maprMessages)
		<-done
	}
}
