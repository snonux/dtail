package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/protocol"
	user "github.com/mimecast/dtail/internal/user/server"
)

type handleCommandCb func(context.Context, lcontext.LContext, int, []string, string)

// commandCancelKeyType is a private key type for stashing a per-command
// context.CancelFunc inside a context.Context. It is used to hand the cancel
// ownership from handleCommand (which creates the context) to the command
// completion callback (handleUserCommand.commandFinished), which is the only
// place that knows when the asynchronous command is actually done.
type commandCancelKeyType struct{}

var commandCancelKey commandCancelKeyType

// withCommandCancel returns a derived context that carries the per-command
// cancel func. See cancelCommandContext for the matching consumer.
func withCommandCancel(ctx context.Context, cancel context.CancelFunc) context.Context {
	if cancel == nil {
		return ctx
	}
	return context.WithValue(ctx, commandCancelKey, cancel)
}

// cancelCommandContext invokes the per-command cancel func stashed on ctx (if
// any) exactly once. It is a no-op when ctx carries no cancel (for example in
// the session-command path where the session state owns the cancel).
func cancelCommandContext(ctx context.Context) {
	cancel, ok := ctx.Value(commandCancelKey).(context.CancelFunc)
	if !ok || cancel == nil {
		return
	}
	cancel()
}

type baseHandler struct {
	done             *internal.Done
	handleCommandCb  handleCommandCb
	lines            chan *line.Line
	aggregate        *server.Aggregate
	turboAggregate   *server.TurboAggregate // Turbo mode aggregate
	maprMessages     chan string
	serverMessages   chan string
	hostname         string
	user             *user.User
	ackCloseReceived chan struct{}
	activeCommands   int32
	codec            protocolCodec
	readBuf          bytes.Buffer
	writeBuf         bytes.Buffer

	// Some global options + sync primitives required.
	once       sync.Once
	mutex      sync.Mutex
	quiet      bool
	plain      bool
	serverless bool

	turbo turboManager

	activeGeneration func() uint64
}

// Shutdown the handler.
func (h *baseHandler) Shutdown() {
	// Shutdown turbo aggregate if present
	if h.turboAggregate != nil {
		dlog.Server.Info(h.user, "Shutting down turbo aggregate")
		h.turboAggregate.Shutdown()
	}
	// Shutdown regular aggregate if present
	if h.aggregate != nil {
		dlog.Server.Info(h.user, "Shutting down regular aggregate")
		h.aggregate.Shutdown()
	}
	h.done.Shutdown()
}

// Done channel of the handler.
func (h *baseHandler) Done() <-chan struct{} {
	return h.done.Done()
}

// Read is to send data to the dtail client via Reader interface.
func (h *baseHandler) Read(p []byte) (n int, err error) {
	defer h.readBuf.Reset()

	for {
		if n, handled := h.turbo.tryRead(p, h.user, h.shouldDropGeneration); handled {
			if n == 0 {
				continue
			}
			return n, nil
		}

		pollInterval := time.Second
		if h.turbo.enabled() {
			// Turbo reads require tighter wake-ups so we can continue draining the turbo channel.
			pollInterval = h.turbo.resolvedReadRetryInterval()
		}
		poll := time.After(pollInterval)

		select {
		case message := <-h.serverMessages:
			generation, decodedMessage := decodeGeneratedMessage(message)
			if h.shouldDropGeneration(generation) {
				continue
			}
			message = decodedMessage
			if len(message) > 0 && message[0] == '.' {
				// Handle hidden message (don't display to the user)
				h.readBuf.WriteString(message)
				h.readBuf.WriteByte(protocol.MessageDelimiter)
				n = copy(p, h.readBuf.Bytes())
				return
			}

			if h.serverless {
				return
			}

			// Skip empty server messages when in plain mode
			if h.plain && (message == "" || message == "\n") {
				return
			}

			// Handle normal server message (display to the user).
			formatServerMessage(&h.readBuf, h.hostname, message, h.plain)
			n = copy(p, h.readBuf.Bytes())
			return

		case message := <-h.maprMessages:
			generation, decodedMessage := decodeGeneratedMessage(message)
			if h.shouldDropGeneration(generation) {
				continue
			}
			message = decodedMessage
			// Send mapreduce-aggregated data as a message.
			h.readBuf.WriteString("AGGREGATE")
			h.readBuf.WriteString(protocol.FieldDelimiter)
			h.readBuf.WriteString(h.hostname)
			h.readBuf.WriteString(protocol.FieldDelimiter)
			h.readBuf.WriteString(message)
			h.readBuf.WriteByte(protocol.MessageDelimiter)
			n = copy(p, h.readBuf.Bytes())
			return

		case line := <-h.lines:
			if line == nil {
				continue
			}
			if h.shouldDropGeneration(line.Generation) {
				pool.RecycleBytesBuffer(line.Content)
				line.Recycle()
				continue
			}
			if h.plain {
				h.readBuf.Write(line.Content.Bytes())
				h.readBuf.WriteByte(protocol.MessageDelimiter)
			} else {
				formatRemoteLine(
					&h.readBuf,
					h.hostname,
					fmt.Sprintf("%3d", line.TransmittedPerc),
					line.Count,
					line.SourceID,
					line.Content.Bytes(),
				)
			}
			n = copy(p, h.readBuf.Bytes())
			pool.RecycleBytesBuffer(line.Content)
			line.Recycle()
			return

		case <-h.done.Done():
			err = io.EOF
			return

		case <-poll:
			// Wake periodically so turbo mode transitions don't leave this read blocked forever.
			select {
			case <-h.done.Done():
				err = io.EOF
				return
			default:
			}
			return
		}
	}
}

// Write is to receive data from the dtail client via Writer interface.
func (h *baseHandler) Write(p []byte) (n int, err error) {
	for _, b := range p {
		switch b {
		case ';':
			h.handleCommand(h.writeBuf.String())
			h.writeBuf.Reset()
		default:
			h.writeBuf.WriteByte(b)
		}
	}
	n = len(p)
	return
}

func (h *baseHandler) handleCommand(commandStr string) {
	dlog.Server.Debug(h.user, commandStr)

	args, argc, add, err := h.handleProtocolVersion(strings.Split(commandStr, " "))
	if err != nil {
		h.send(h.serverMessages, dlog.Server.Error(h.user, err)+add)
		return
	}
	args, argc, err = h.handleBase64(args, argc)
	if err != nil {
		h.sendln(h.serverMessages, dlog.Server.Error(h.user, err))
		return
	}
	ctx, cancel := h.newCommandContext(context.Background())
	// Cancel ownership is transferred to the command completion callback
	// (see cancelCommandContext + handleUserCommand.commandFinished) so the
	// per-command context and its watcher goroutine are released once the
	// (possibly asynchronous) command has finished. If dispatch fails before
	// the callback is ever invoked we must cancel here to avoid a leak.
	ctx = withCommandCancel(ctx, cancel)

	if err := h.dispatchCommand(ctx, args, argc); err != nil {
		cancel()
		h.sendln(h.serverMessages, dlog.Server.Error(h.user, err))
	}
}

func (h *baseHandler) dispatchCommand(ctx context.Context, args []string, argc int) error {
	parts := strings.Split(args[0], ":")
	commandName := parts[0]

	// Either no options or empty options provided.
	if len(parts) == 1 || len(parts[1]) == 0 {
		h.handleCommandCb(ctx, lcontext.LContext{}, argc, args, commandName)
		return nil
	}

	options, ltx, err := config.DeserializeOptions(parts[1:])
	if err != nil {
		return err
	}
	h.handleOptions(options)
	h.handleCommandCb(ctx, ltx, argc, args, commandName)
	return nil
}

func (h *baseHandler) handleProtocolVersion(args []string) ([]string, int, string, error) {
	return h.codec.handleProtocolVersion(args)
}

func (h *baseHandler) handleBase64(args []string, argc int) ([]string, int, error) {
	return h.codec.handleBase64(args, argc)
}

func (h *baseHandler) handleRawCommand(ctx context.Context, command string) error {
	args := strings.Fields(command)
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	return h.dispatchCommand(ctx, args, len(args))
}

// newCommandContext creates a cancellable context for a single command
// invocation. The caller owns the returned cancel func and MUST invoke it
// exactly once (typically via defer or through the per-command cancel
// stashed on the context, see withCommandCancel/cancelCommandContext).
// Failing to cancel leaks both the context and the watcher goroutine
// spawned below, because the watcher only returns when the handler is shut
// down; on long-lived sessions (:reload, continuous/scheduled workloads)
// those leaks accumulate per command.
//
// The watcher goroutine doubles as a defensive safety net: even if a
// caller forgets to cancel, handler shutdown still drains it by cancelling
// the context via <-h.done.Done().
func (h *baseHandler) newCommandContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-h.done.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (h *baseHandler) handleAckCommand(argc int, args []string) {
	if argc < 3 {
		if !h.quiet {
			h.sendln(h.serverMessages, dlog.Server.Warn(h.user,
				"Unable to parse command", args, argc))
		}
		return
	}
	if args[1] == "close" && args[2] == "connection" {
		select {
		case <-h.ackCloseReceived:
		default:
			close(h.ackCloseReceived)
		}
	}
}

func (h *baseHandler) handleOptions(options map[string]string) {
	// We have to make sure that this block is executed only once.
	h.mutex.Lock()
	defer h.mutex.Unlock()
	// We can read the options only once, will cause a data race otherwise if
	// changed multiple times for multiple incoming commands.
	h.once.Do(func() {
		if quiet := options["quiet"]; quiet == "true" {
			dlog.Server.Debug(h.user, "Enabling quiet mode")
			h.quiet = true
		}
		if plain := options["plain"]; plain == "true" {
			dlog.Server.Debug(h.user, "Enabling plain mode")
			h.plain = true
		}
		if serverless := options["serverless"]; serverless == "true" {
			dlog.Server.Debug(h.user, "Enabling serverless mode")
			h.serverless = true
		}
	})
}

func (h *baseHandler) send(ch chan<- string, message string) {
	select {
	case ch <- message:
	case <-h.done.Done():
	}
}

func (h *baseHandler) sendln(ch chan<- string, message string) {
	h.send(ch, message+"\n")
}

func (h *baseHandler) shouldDropGeneration(generation uint64) bool {
	if generation == 0 || h.activeGeneration == nil {
		return false
	}

	activeGeneration := h.activeGeneration()
	if activeGeneration == 0 {
		return false
	}

	return activeGeneration != generation
}

func (h *baseHandler) flush() {
	dlog.Server.Trace(h.user, "flush()")
	numUnsentMessages := func() int {
		lineCount := len(h.lines)
		serverCount := len(h.serverMessages)
		maprCount := len(h.maprMessages)
		turboCount := h.turbo.channelLen()
		dlog.Server.Trace(h.user, "flush", "lines", lineCount, "server", serverCount, "mapr", maprCount, "turbo", turboCount)
		return lineCount + serverCount + maprCount + turboCount
	}

	maxWait := time.Second
	if h.turbo.enabled() || h.turboAggregate != nil || h.aggregate != nil {
		maxWait = 3 * time.Second
	}
	if h.serverless && maxWait < 5*time.Second {
		maxWait = 5 * time.Second
	}

	deadline := time.Now().Add(maxWait)
	for i := 0; ; i++ {
		unsent := numUnsentMessages()
		if unsent == 0 {
			dlog.Server.Debug(h.user, "ALL lines sent", fmt.Sprintf("%p", h))
			return
		}
		if time.Now().After(deadline) {
			dlog.Server.Warn(h.user, "Some lines remain unsent", unsent)
			return
		}
		dlog.Server.Debug(h.user, "Still lines to be sent", "iteration", i, "unsent", unsent, "deadline", deadline.Sub(time.Now()))
		time.Sleep(time.Millisecond * 10)
	}
}

func (h *baseHandler) shutdown() {
	// Log current state at shutdown
	activeCommands := atomic.LoadInt32(&h.activeCommands)
	dlog.Server.Info(h.user, "shutdown() called", "activeCommands", activeCommands, "turboMode", h.turbo.enabled())

	// In turbo mode, ensure all data is flushed before shutdown
	if h.turbo.enabled() {
		h.flushTurboData()
	}

	// Shutdown aggregates BEFORE flush to ensure MapReduce data is available
	if h.turboAggregate != nil {
		dlog.Server.Info(h.user, "Shutting down turbo aggregate in shutdown()")
		h.turboAggregate.Shutdown()
		// Give time for serialization to complete
		time.Sleep(100 * time.Millisecond)
	}
	if h.aggregate != nil {
		dlog.Server.Info(h.user, "Shutting down regular aggregate in shutdown()")
		h.aggregate.Shutdown()
		// Give time for serialization to complete
		time.Sleep(100 * time.Millisecond)
	}

	h.flush()

	go func() {
		select {
		case h.serverMessages <- ".syn close connection":
		case <-h.done.Done():
		}
	}()

	select {
	case <-h.ackCloseReceived:
	case <-time.After(time.Second * 5):
		dlog.Server.Debug(h.user, "Shutdown timeout reached, enforcing shutdown")
	case <-h.done.Done():
	}
	h.done.Shutdown()
}

func (h *baseHandler) incrementActiveCommands() {
	atomic.AddInt32(&h.activeCommands, 1)
}

func (h *baseHandler) decrementActiveCommands() int32 {
	atomic.AddInt32(&h.activeCommands, -1)
	return atomic.LoadInt32(&h.activeCommands)
}

// EnableTurboMode enables turbo mode for direct line processing
func (h *baseHandler) EnableTurboMode() {
	h.turbo.enable()
}

// IsTurboMode returns true if turbo mode is enabled
func (h *baseHandler) IsTurboMode() bool {
	return h.turbo.enabled()
}

// HasTurboEOF returns true when a turbo EOF channel exists.
func (h *baseHandler) HasTurboEOF() bool {
	return h.turbo.hasEOF()
}

// SignalTurboEOF closes turbo EOF channel once.
func (h *baseHandler) SignalTurboEOF() {
	h.turbo.signalEOF()
}

// flushTurboData ensures all turbo channel data is processed
func (h *baseHandler) flushTurboData() {
	h.turbo.flush(h.user)
}

// GetTurboChannel returns the turbo lines channel for direct writing
func (h *baseHandler) GetTurboChannel() chan []byte {
	return h.turbo.channel()
}

// TurboChannelLen returns current turbo channel buffered size.
func (h *baseHandler) TurboChannelLen() int {
	return h.turbo.channelLen()
}

// WaitForTurboEOFAck waits until turbo reader acknowledges EOF or timeout.
func (h *baseHandler) WaitForTurboEOFAck(timeout time.Duration) bool {
	return h.turbo.waitForEOFAck(timeout)
}
