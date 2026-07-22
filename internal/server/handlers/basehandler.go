package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
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
	maprserver "github.com/mimecast/dtail/internal/mapr/server"
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
	done            *internal.Done
	handleCommandCb handleCommandCb
	lines           chan *line.Line

	// aggregate is written by handleMapCommand on the command-dispatch
	// goroutine and read concurrently by Shutdown, Aggregate, and
	// resetSessionAggregates. Using atomic.Pointer eliminates the data race
	// without requiring h.mutex to be held around every access site.
	aggregate atomic.Pointer[maprserver.Aggregate]

	maprMessages     chan string
	serverMessages   chan string
	hostname         string
	user             *user.User
	ackCloseReceived chan struct{}
	ackCloseOnce     sync.Once
	activeCommands   int32
	codec            protocolCodec

	// readBuf holds the formatted protocol message currently being sent to
	// the client. It is only touched by Read (single session output
	// goroutine) and retains any bytes that did not fit into the caller's
	// buffer, so messages larger than one Read are delivered across multiple
	// calls instead of being truncated (see Read/drainReadBuf).
	readBuf  bytes.Buffer
	writeBuf bytes.Buffer

	// maxCommandFrameSize is the maximum number of bytes that may be buffered
	// between two ';' delimiters. When a frame grows beyond this limit the
	// Write method closes the session immediately to prevent a malicious or
	// misbehaving client from exhausting server memory. The value is set at
	// construction time from ServerConfig.MaxCommandFrameSize.
	maxCommandFrameSize int

	// Some global options + sync primitives required.
	once       sync.Once
	mutex      sync.Mutex
	quiet      bool
	plain      bool
	serverless bool

	output outputManager

	activeGeneration func() uint64
}

// getAggregate returns the current output MapReduce aggregate atomically.
func (h *baseHandler) getAggregate() *maprserver.Aggregate {
	return h.aggregate.Load()
}

// setAggregate stores a output MapReduce aggregate atomically.
func (h *baseHandler) setAggregate(ta *maprserver.Aggregate) {
	h.aggregate.Store(ta)
}

// Shutdown the handler. Uses atomic accessors to read aggregate pointers so
// the reads are race-free with concurrent writes from handleMapCommand.
func (h *baseHandler) Shutdown() {
	// Shutdown output aggregate if present.
	if ta := h.getAggregate(); ta != nil {
		dlog.Server.Info(h.user, "Shutting down output aggregate")
		ta.Shutdown()
	}
	h.done.Shutdown()
}

// Done channel of the handler.
func (h *baseHandler) Done() <-chan struct{} {
	return h.done.Done()
}

// Read is to send data to the dtail client via Reader interface.
//
// A formatted protocol message can be larger than p (io.Copy drives this
// reader with a 32KB buffer while MaxLineLength allows lines up to 1MB), so
// each Read drains any bytes left over from a previous call first and every
// message path keeps its unsent remainder in readBuf across calls. Dropping
// the remainder would truncate long lines and lose the trailing message
// delimiter, desyncing the client-side parser. This mirrors the remainder
// buffer used by the output path (outputManager.tryRead).
func (h *baseHandler) Read(p []byte) (n int, err error) {
	if h.readBuf.Len() > 0 {
		return h.drainReadBuf(p), nil
	}

	for {
		if n, handled := h.output.tryRead(p, h.user, h.shouldDropGeneration); handled {
			if n == 0 {
				continue
			}
			return n, nil
		}

		pollInterval := time.Second
		if h.output.enabled() {
			// Output reads require tighter wake-ups so we can continue draining the output channel.
			pollInterval = h.output.resolvedReadRetryInterval()
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
				n = h.drainReadBuf(p)
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
			n = h.drainReadBuf(p)
			return

		case message := <-h.maprMessages:
			generation, decodedMessage := decodeGeneratedMessage(message)
			if h.shouldDropGeneration(generation) {
				continue
			}
			message = decodedMessage
			// Send mapreduce-aggregated data as a message. The leading
			// AggregateMessageID field lets the mapr client tell aggregate
			// data apart from plain server acks that happen to start with 'A'.
			h.readBuf.WriteString(protocol.AggregateMessageID)
			h.readBuf.WriteString(protocol.FieldDelimiter)
			h.readBuf.WriteString(h.hostname)
			h.readBuf.WriteString(protocol.FieldDelimiter)
			h.readBuf.WriteString(message)
			h.readBuf.WriteByte(protocol.MessageDelimiter)
			n = h.drainReadBuf(p)
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
			n = h.drainReadBuf(p)
			pool.RecycleBytesBuffer(line.Content)
			line.Recycle()
			return

		case <-h.done.Done():
			err = io.EOF
			return

		case <-poll:
			// Wake periodically so output mode transitions don't leave this read blocked forever.
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

// drainReadBuf copies as many buffered message bytes as fit into p and keeps
// the remainder in readBuf for subsequent Read calls. bytes.Buffer.Read
// consumes exactly the bytes it returns, so nothing is ever discarded; its
// io.EOF (only possible on an empty buffer) is deliberately not propagated
// because an empty buffer here simply means there is nothing left to drain.
func (h *baseHandler) drainReadBuf(p []byte) int {
	n, _ := h.readBuf.Read(p)
	return n
}

// Write is to receive data from the dtail client via Writer interface.
// Each byte is accumulated in writeBuf until a ';' delimiter arrives, at which
// point the buffered frame is dispatched as a command and the buffer is reset.
//
// To prevent a client from exhausting server memory with an unterminated frame,
// the buffer length is checked against maxCommandFrameSize on every append. When
// the limit is exceeded the session is shut down and io.ErrClosedPipe is returned
// so the SSH layer tears down the connection.
func (h *baseHandler) Write(p []byte) (n int, err error) {
	for _, b := range p {
		switch b {
		case ';':
			h.handleCommand(h.writeBuf.String())
			h.writeBuf.Reset()
		default:
			h.writeBuf.WriteByte(b)
			// Guard against unbounded frame growth: a client could send bytes
			// without ever emitting a ';' delimiter and grow the buffer
			// indefinitely. Reject and close when the configurable limit is hit.
			if h.maxCommandFrameSize > 0 && h.writeBuf.Len() > h.maxCommandFrameSize {
				dlog.Server.Error(h.user,
					"command frame exceeds maximum size, closing session",
					"frameSize", h.writeBuf.Len(),
					"limit", h.maxCommandFrameSize,
				)
				h.writeBuf.Reset()
				h.done.Shutdown()
				return len(p), io.ErrClosedPipe
			}
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
	// Strip and apply a leading "timeout N <cmd>..." prefix. The client emits
	// this when --timeout>0 (see internal/session/spec.go queryCommands); it
	// caps how long the server collects data for that read command before its
	// context is canceled. Handling it here covers both the legacy command
	// stream and the SESSION dispatch path, which both funnel through here.
	ctx, args, argc, err := applyCommandTimeout(ctx, args, argc)
	if err != nil {
		return err
	}

	parts := strings.SplitN(args[0], ":", 2)
	commandName := parts[0]

	// Either no options or empty options provided.
	if len(parts) == 1 || len(parts[1]) == 0 {
		h.handleCommandCb(ctx, lcontext.LContext{}, argc, args, commandName)
		return nil
	}

	options, ltx, err := config.DeserializeOptions([]string{parts[1]})
	if err != nil {
		return err
	}
	h.handleOptions(options)
	h.handleCommandCb(ctx, ltx, argc, args, commandName)
	return nil
}

// maxCommandTimeoutSeconds caps the "timeout N <cmd>" prefix value. 24h is far
// beyond any realistic collection window yet nowhere near the int64 overflow
// point of time.Duration (~292 years in nanoseconds), so it doubles as an
// overflow guard for the multiplication in applyCommandTimeout.
const maxCommandTimeoutSeconds = 24 * 60 * 60

// applyCommandTimeout detects a leading "timeout N <cmd>..." command prefix (as
// emitted by the client when --timeout>0) and returns a context that is
// canceled after N seconds together with the remaining command (the prefix
// stripped). This restores the original server-side deadline semantics: "Max
// time dtail server will collect data until disconnection". When no timeout
// prefix is present, or N<=0, the context and args are returned unchanged so
// the --timeout 0 / unset case behaves exactly as before.
//
// The timeout child cancel is chained onto the per-command cancel already
// stashed on ctx (if any) so cancelCommandContext, invoked once the command
// finishes, releases both the parent cancel and the timeout timer. In the
// session-dispatch path ctx carries no per-command cancel, so the returned
// context is the sole owner and its cancel still fires on command completion.
func applyCommandTimeout(ctx context.Context, args []string, argc int) (context.Context, []string, int, error) {
	if argc < 3 || args[0] != "timeout" {
		return ctx, args, argc, nil
	}

	seconds, err := strconv.Atoi(args[1])
	if err != nil {
		return ctx, args, argc, fmt.Errorf("invalid timeout value %q: %w", args[1], err)
	}
	// Reject absurd values rather than clamp: an out-of-range N is a client
	// mistake, and erroring (like the non-numeric case above) surfaces it
	// instead of silently substituting a different deadline. This also guards
	// against int64 overflow in time.Duration(seconds)*time.Second below, which
	// for a huge N would wrap to a negative (already-elapsed) deadline and
	// cancel the read immediately.
	if seconds > maxCommandTimeoutSeconds {
		return ctx, args, argc, fmt.Errorf("timeout value %d exceeds maximum of %d seconds",
			seconds, maxCommandTimeoutSeconds)
	}
	if seconds <= 0 {
		return ctx, args[2:], argc - 2, nil
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	parentCancel, _ := ctx.Value(commandCancelKey).(context.CancelFunc)
	combined := func() {
		cancel()
		if parentCancel != nil {
			parentCancel()
		}
	}

	return withCommandCancel(timeoutCtx, combined), args[2:], argc - 2, nil
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
		h.ackCloseOnce.Do(func() {
			close(h.ackCloseReceived)
		})
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
		outputCount := h.output.channelLen()
		dlog.Server.Trace(h.user, "flush", "lines", lineCount, "server", serverCount, "mapr", maprCount, "output", outputCount)
		return lineCount + serverCount + maprCount + outputCount
	}

	// Use atomic accessors to avoid a data race with handleMapCommand, which
	// may be concurrently writing aggregate pointers on another goroutine.
	maxWait := time.Second
	if h.output.enabled() || h.getAggregate() != nil {
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
	dlog.Server.Info(h.user, "shutdown() called", "activeCommands", activeCommands, "outputMode", h.output.enabled())

	// In output mode, ensure all data is flushed before shutdown
	if h.output.enabled() {
		h.flushOutput()
	}

	// Shutdown the aggregate BEFORE flush to ensure MapReduce data is available.
	// Use the atomic accessor to avoid a data race with handleMapCommand which
	// may be concurrently storing the aggregate pointer on another goroutine.
	if ta := h.getAggregate(); ta != nil {
		dlog.Server.Info(h.user, "Shutting down output aggregate in shutdown()")
		ta.Shutdown()
		// Give time for serialization to complete.
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

// EnableDirectOutput enables output mode for direct line processing. It is an
// atomic check-and-enable: the return value is true when this call switched
// output mode on and false when it was already active (in which case the
// existing output state is left untouched).
func (h *baseHandler) EnableDirectOutput() bool {
	return h.output.enable()
}

// DirectOutputActive returns true if output mode is enabled
func (h *baseHandler) DirectOutputActive() bool {
	return h.output.enabled()
}

// HasOutputEOF returns true when a output EOF channel exists.
func (h *baseHandler) HasOutputEOF() bool {
	return h.output.hasEOF()
}

// OutputEpoch returns the current output handshake epoch. Capture it before
// checking the pending-work count and pass it to SignalOutputEOF so a stale
// "batch over" decision cannot EOF a batch that joined in between.
func (h *baseHandler) OutputEpoch() uint64 {
	return h.output.currentEpoch()
}

// SignalOutputEOF closes the output EOF channel once, unless the handshake
// epoch has advanced past the given captured value (i.e. another command
// joined the output session since), in which case the stale signal is dropped.
func (h *baseHandler) SignalOutputEOF(epoch uint64) {
	h.output.signalEOF(epoch)
}

// flushOutput ensures all output channel data is processed
func (h *baseHandler) flushOutput() {
	h.output.flush(h.user)
}

// GetOutputChannel returns the output lines channel for direct writing
func (h *baseHandler) GetOutputChannel() chan []byte {
	return h.output.channel()
}

// OutputChannelLen returns current output channel buffered size.
func (h *baseHandler) OutputChannelLen() int {
	return h.output.channelLen()
}

// WaitForOutputEOFAck waits until output reader acknowledges EOF or timeout.
func (h *baseHandler) WaitForOutputEOFAck(timeout time.Duration) bool {
	return h.output.waitForEOFAck(timeout)
}
