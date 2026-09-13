package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/logging"
	maprserver "github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/protocol"
	user "github.com/mimecast/dtail/internal/user/server"
)

type handleCommandCb func(context.Context, lcontext.LContext, int, []string, string)
type prepareCommandContextCb func(context.Context, string) (context.Context, func())

// commandCancelKeyType is a private key type for stashing a per-command
// context.CancelFunc inside a context.Context. It is used to hand the cancel
// ownership from handleCommand (which creates the context) to the command
// completion callback (handleUserCommand.commandFinished), which is the only
// place that knows when the asynchronous command is actually done.
type commandCancelKeyType struct{}

var commandCancelKey commandCancelKeyType
var errHandlerFlushTimeout = errors.New("timed out flushing session output")

// sessionCommandAdmissionKey marks work dispatched by a SESSION command whose
// outer transaction has already passed command admission. Graceful shutdown
// must let that synchronous nested dispatch finish before it waits, even after
// sealing admission to unrelated commands.
type sessionCommandAdmissionKeyType struct{}

var sessionCommandAdmissionKey sessionCommandAdmissionKeyType

type commandAdmissionResultKeyType struct{}

var commandAdmissionResultKey commandAdmissionResultKeyType

type commandAdmissionResult struct {
	admitted bool
}

func withSessionCommandAdmission(ctx context.Context) context.Context {
	return context.WithValue(ctx, sessionCommandAdmissionKey, true)
}

func hasSessionCommandAdmission(ctx context.Context) bool {
	admitted, _ := ctx.Value(sessionCommandAdmissionKey).(bool)
	return admitted
}

func markCommandAdmitted(ctx context.Context) {
	if result, ok := ctx.Value(commandAdmissionResultKey).(*commandAdmissionResult); ok {
		result.admitted = true
	}
}

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
	logger                  logging.Logger
	readerLogger            logging.Logger
	serverlessOutput        io.Writer
	done                    *internal.Done
	handleCommandCb         handleCommandCb
	prepareCommandContextCb prepareCommandContextCb

	// aggregate is written by handleMapCommand on the command-dispatch
	// goroutine and read concurrently by Shutdown, Aggregate, and
	// resetSessionAggregates. Using atomic.Pointer eliminates the data race
	// without requiring h.mutex to be held around every access site.
	aggregate atomic.Pointer[maprserver.Aggregate]

	maprMessages      chan string
	serverMessages    chan string
	hostname          string
	user              *user.User
	ackCloseReceived  chan struct{}
	ackCloseOnce      sync.Once
	flushInitOnce     sync.Once
	flushRequests     chan chan struct{}
	flushErrors       chan string
	closeSyncWake     chan struct{}
	pendingFlushAcks  []chan struct{}
	deferredServer    string
	hasDeferredServer bool
	closeSyncObserved bool
	closeSyncEmitted  bool
	activeCommands    int32
	codec             protocolCodec
	commandDone       *internal.Done
	outputAbort       *internal.Done
	commandMu         sync.Mutex
	commandInitWg     sync.WaitGroup
	commandWg         sync.WaitGroup
	stopping          bool
	aborting          bool

	// readBuf holds the formatted protocol message currently being sent to
	// the client. It is only touched by Read (single session output
	// goroutine) and retains any bytes that did not fit into the caller's
	// buffer, so messages larger than one Read are delivered across multiple
	// calls instead of being truncated (see Read/drainReadBuf).
	readBuf  bytes.Buffer
	writeBuf bytes.Buffer
	// readActive and readBufferedBytes let shutdown distinguish an idle handler
	// from a slow client whose session reader still owns a partially delivered
	// protocol message. readBuf itself remains single-reader owned.
	readActive         atomic.Bool
	readerSeen         atomic.Bool
	readBufferedBytes  atomic.Int64
	deliveryPending    atomic.Bool
	closeSyncRequested atomic.Bool

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

// Logger returns the handler logger, falling back to a no-op logger for
// zero-value handlers used by focused unit tests.
func (h *baseHandler) Logger() logging.Logger {
	return logging.OrNop(h.logger)
}

// ReaderLogger returns the logger used by filesystem reader dependencies.
func (h *baseHandler) ReaderLogger() logging.Logger {
	return logging.OrNop(h.readerLogger)
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
	// A transport shutdown cannot depend on an output reader still being
	// present. Mark aggregate output abandoned before canceling command work so
	// a racing Aggregate.Start never attempts a final send to a dead peer.
	h.commandMu.Lock()
	h.stopping = true
	h.aborting = true
	if h.outputAbort != nil {
		h.outputAbort.Shutdown()
	}
	if ta := h.getAggregate(); ta != nil {
		ta.Abort()
	}
	if h.commandDone != nil {
		h.commandDone.Shutdown()
	}
	h.commandMu.Unlock()

	if ta := h.getAggregate(); ta != nil {
		h.Logger().Info(h.user, "Aborting output aggregate")
		ta.AbortAndWait()
	}
	h.done.Shutdown()
	h.commandWg.Wait()
}

// Done channel of the handler.
func (h *baseHandler) Done() <-chan struct{} {
	return h.done.Done()
}

// AttachOutputReader declares that the handler's output is owned by a
// transport reader. The server calls this before starting either session I/O
// goroutine so a fast input command cannot reach flush before Read starts.
// In-process consumers that drain the raw channels directly leave it unset.
func (h *baseHandler) AttachOutputReader() {
	h.readerSeen.Store(true)
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
	h.ensureFlushChannels()
	h.readerSeen.Store(true)
	h.readActive.Store(true)
	// io.Copy never calls Read again until its Writer accepted the previous
	// bytes. Entering the next Read therefore confirms that delivery; the defer
	// below publishes a new pending delivery before clearing readActive.
	h.deliveryPending.Store(false)
	defer func() {
		if n > 0 {
			h.deliveryPending.Store(true)
		}
		h.readActive.Store(false)
	}()
	if h.readBuf.Len() > 0 {
		return h.drainReadBuf(p), nil
	}

	for {
		// Snapshot the output notification channel before inspecting the queue.
		// If a producer changes state between these two operations it leaves a
		// token on this channel, so the blocking select wakes without polling.
		outputChanged, eofWait := h.output.readWait()
		if readN, handled := h.output.tryRead(p, h.user, h.shouldDropGeneration); handled {
			if readN == 0 {
				continue
			}
			return readN, nil
		}
		if readN, handled := h.tryReadQueued(p); handled {
			if readN == 0 {
				continue
			}
			return readN, nil
		}

		var eofTimer *time.Timer
		var eofTimerC <-chan time.Time
		if eofWait > 0 {
			eofTimer = time.NewTimer(eofWait)
			eofTimerC = eofTimer.C
		}

		select {
		case message := <-h.flushErrors:
			stopOptionalTimer(eofTimer)
			n = h.readServerMessage(p, message)
			if n == 0 {
				continue
			}
			return n, nil

		case message := <-h.serverMessages:
			stopOptionalTimer(eofTimer)
			n = h.readSelectedServerMessage(p, message)
			if n == 0 {
				continue
			}
			return n, nil

		case message := <-h.maprMessages:
			stopOptionalTimer(eofTimer)
			n = h.readMaprMessage(p, message)
			if n == 0 {
				continue
			}
			return n, nil

		case <-h.done.Done():
			stopOptionalTimer(eofTimer)
			// Producers finish before graceful shutdown closes done. Recheck
			// every queue after observing it so EOF can never overtake a final
			// protocol message that was ready in the same select.
			if n, handled := h.output.tryRead(p, h.user, h.shouldDropGeneration); handled {
				if n == 0 {
					continue
				}
				return n, nil
			}
			if n, handled := h.tryReadQueued(p); handled {
				if n == 0 {
					continue
				}
				return n, nil
			}
			return 0, io.EOF

		case flushAck := <-h.flushRequests:
			stopOptionalTimer(eofTimer)
			h.pendingFlushAcks = append(h.pendingFlushAcks, flushAck)
			continue

		case <-outputChanged:
			stopOptionalTimer(eofTimer)
			continue

		case <-h.closeSyncWake:
			stopOptionalTimer(eofTimer)
			h.closeSyncObserved = true
			continue

		case <-eofTimerC:
			// The EOF quiet period is a protocol deadline rather than a queue
			// poll. Recheck once so maybeAckEOFLocked can complete the handshake.
			continue
		}
	}
}

func (h *baseHandler) tryReadQueued(p []byte) (int, bool) {
	select {
	case message := <-h.flushErrors:
		return h.readServerMessage(p, message), true
	default:
	}
	if h.hasDeferredServer {
		message := h.deferredServer
		h.deferredServer = ""
		h.hasDeferredServer = false
		return h.readServerMessage(p, message), true
	}
	select {
	case message := <-h.serverMessages:
		return h.readServerMessage(p, message), true
	default:
	}
	select {
	case message := <-h.maprMessages:
		return h.readMaprMessage(p, message), true
	default:
	}
	// A flush request is acknowledged only by the session reader, after it has
	// returned every prior read to io.Copy's writer and observed all queues and
	// the output manager empty. Keeping an acknowledgement pending across Read
	// calls also covers protocol messages that span the caller's buffer.
	receivedFlushRequest := false
	for {
		select {
		case flushAck := <-h.flushRequests:
			h.pendingFlushAcks = append(h.pendingFlushAcks, flushAck)
			receivedFlushRequest = true
		default:
			if receivedFlushRequest {
				// The queue scan above preceded receipt of this request. Start a
				// fresh pass so output published before or during the handoff cannot
				// be overtaken by its acknowledgement.
				return 0, true
			}
			if h.closeSyncRequested.Load() && !h.closeSyncObserved {
				h.closeSyncObserved = true
				// A timeout error or final producer payload may have arrived with
				// the terminal request after the scan above. Recheck every source
				// before acknowledging barriers or emitting close.
				return 0, true
			}
			for _, flushAck := range h.pendingFlushAcks {
				close(flushAck)
			}
			h.pendingFlushAcks = nil
			if h.closeSyncObserved && h.closeSyncRequested.Load() && !h.closeSyncEmitted {
				h.closeSyncEmitted = true
				return h.readServerMessage(p, ".syn close connection"), true
			}
			return 0, false
		}
	}
}

// readSelectedServerMessage preserves flush-error priority even when Go's
// select chooses the normal server channel after both it and flushErrors became
// ready. The selected message remains reader-owned and is delivered on the next
// Read, so `.syn close connection` cannot overtake the error it follows.
func (h *baseHandler) readSelectedServerMessage(p []byte, message string) int {
	select {
	case flushError := <-h.flushErrors:
		h.deferredServer = message
		h.hasDeferredServer = true
		return h.readServerMessage(p, flushError)
	default:
		return h.readServerMessage(p, message)
	}
}

func (h *baseHandler) readServerMessage(p []byte, message string) int {
	generation, decodedMessage := decodeGeneratedMessage(message)
	if h.shouldDropGeneration(generation) {
		return 0
	}
	message = decodedMessage
	if len(message) > 0 && message[0] == '.' {
		h.readBuf.WriteString(message)
		h.readBuf.WriteByte(protocol.MessageDelimiter)
		return h.drainReadBuf(p)
	}
	if h.serverless || h.plain && (message == "" || message == "\n") {
		return 0
	}
	formatServerMessage(&h.readBuf, h.hostname, message, h.plain)
	return h.drainReadBuf(p)
}

func (h *baseHandler) readMaprMessage(p []byte, message string) int {
	generation, decodedMessage := decodeGeneratedMessage(message)
	if h.shouldDropGeneration(generation) {
		return 0
	}
	h.readBuf.WriteString(protocol.AggregateMessageID)
	h.readBuf.WriteString(protocol.FieldDelimiter)
	h.readBuf.WriteString(h.hostname)
	h.readBuf.WriteString(protocol.FieldDelimiter)
	h.readBuf.WriteString(decodedMessage)
	h.readBuf.WriteByte(protocol.MessageDelimiter)
	return h.drainReadBuf(p)
}

// drainReadBuf copies as many buffered message bytes as fit into p and keeps
// the remainder in readBuf for subsequent Read calls. bytes.Buffer.Read
// consumes exactly the bytes it returns, so nothing is ever discarded; its
// io.EOF (only possible on an empty buffer) is deliberately not propagated
// because an empty buffer here simply means there is nothing left to drain.
func (h *baseHandler) drainReadBuf(p []byte) int {
	n, _ := h.readBuf.Read(p)
	h.readBufferedBytes.Store(int64(h.readBuf.Len()))
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
				h.Logger().Error(h.user,
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
	h.Logger().Debug(h.user, commandStr)

	args, argc, add, err := h.handleProtocolVersion(strings.Split(commandStr, " "))
	if err != nil {
		h.send(h.serverMessages, h.Logger().Error(h.user, err)+add)
		return
	}
	args, argc, err = h.handleBase64(args, argc)
	if err != nil {
		h.sendln(h.serverMessages, h.Logger().Error(h.user, err))
		return
	}
	ctx, cancel := h.newCommandContext(context.Background())
	// Cancel ownership is transferred to the command completion callback
	// (see cancelCommandContext + handleUserCommand.commandFinished) so the
	// per-command context and its watcher goroutine are released once the
	// (possibly asynchronous) command has finished. If dispatch fails before
	// the callback is ever invoked we must cancel here to avoid a leak.
	ctx = withCommandCancel(ctx, cancel)

	if dispatchErr := h.dispatchCommand(ctx, args, argc); dispatchErr != nil {
		cancel()
		h.sendln(h.serverMessages, h.Logger().Error(h.user, dispatchErr))
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
	ltx := lcontext.LContext{}

	if len(parts) == 2 && len(parts[1]) != 0 {
		options, parsedContext, deserializeErr := config.DeserializeOptions([]string{parts[1]})
		if deserializeErr != nil {
			return deserializeErr
		}
		h.handleOptions(options)
		ltx = parsedContext
	}

	// Reserve read input only after all synchronous parsing succeeds, but
	// before handleUserCommand can launch an asynchronous command goroutine.
	// Parse failures never enter command admission and therefore must not own a
	// pending token or an idle-shutdown transition.
	if h.prepareCommandContextCb != nil {
		var cleanup func()
		ctx, cleanup = h.prepareCommandContextCb(ctx, commandName)
		if cleanup != nil {
			defer cleanup()
		}
	}

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
	commandDone := h.done.Done()
	if h.commandDone != nil {
		commandDone = h.commandDone.Done()
	}
	go func() {
		defer recoverHandlerPanic(h.Logger(), h.user, "command cancellation watcher", h.abortAfterPanic)
		defer cancel()
		select {
		case <-commandDone:
		case <-h.done.Done():
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (h *baseHandler) handleAckCommand(argc int, args []string) {
	if argc < 3 {
		if !h.quiet {
			h.sendln(h.serverMessages, h.Logger().Warn(h.user,
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
			h.Logger().Debug(h.user, "Enabling quiet mode")
			h.quiet = true
		}
		if plain := options["plain"]; plain == "true" {
			h.Logger().Debug(h.user, "Enabling plain mode")
			h.plain = true
		}
		if serverless := options["serverless"]; serverless == "true" {
			h.Logger().Debug(h.user, "Enabling serverless mode")
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

func (h *baseHandler) ensureFlushChannels() {
	h.flushInitOnce.Do(func() {
		if h.flushRequests == nil {
			h.flushRequests = make(chan chan struct{})
		}
		if h.flushErrors == nil {
			h.flushErrors = make(chan string, 2)
		}
		if h.closeSyncWake == nil {
			h.closeSyncWake = make(chan struct{}, 1)
		}
	})
}

func (h *baseHandler) flush() error {
	return h.flushContext(context.Background())
}

func (h *baseHandler) flushContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	h.ensureFlushChannels()
	h.Logger().Trace(h.user, "flush()")
	numUnsentMessages := func() int {
		serverCount := len(h.serverMessages)
		maprCount := len(h.maprMessages)
		outputBytes := h.output.bufferedLen()
		h.Logger().Trace(h.user, "flush", "server", serverCount, "mapr", maprCount, "outputBytes", outputBytes)
		return serverCount + maprCount + outputBytes
	}

	maxWait := h.output.resolvedFlushTimeout()
	// Some in-process/serverless users consume the exposed queues directly and
	// never attach the transport Reader. There is no reader to acknowledge in
	// that mode; queue ownership remains with that external consumer.
	if !h.readerSeen.Load() {
		return nil
	}
	if numUnsentMessages() == 0 && h.readBufferedBytes.Load() == 0 &&
		!h.readActive.Load() && !h.deliveryPending.Load() {
		h.Logger().Debug(h.user, "ALL lines sent", fmt.Sprintf("%p", h))
		return nil
	}

	flushAck := make(chan struct{})
	timer := time.NewTimer(maxWait)
	defer timer.Stop()

	select {
	case h.flushRequests <- flushAck:
	case <-ctx.Done():
		return fmt.Errorf("flush session output: %w", ctx.Err())
	case <-h.done.Done():
		return fmt.Errorf("flush session output: handler stopped")
	case <-timer.C:
		return fmt.Errorf("%w after %s: %d queued units remain",
			errHandlerFlushTimeout, maxWait, numUnsentMessages())
	}

	select {
	case <-flushAck:
		h.Logger().Debug(h.user, "ALL lines sent", fmt.Sprintf("%p", h))
		return nil
	case <-ctx.Done():
		return fmt.Errorf("flush session output: %w", ctx.Err())
	case <-h.done.Done():
		return fmt.Errorf("flush session output: handler stopped")
	case <-timer.C:
		select {
		case <-flushAck:
			h.Logger().Debug(h.user, "ALL lines sent", fmt.Sprintf("%p", h))
			return nil
		default:
		}
		return fmt.Errorf("%w after %s: %d queued units remain",
			errHandlerFlushTimeout, maxWait, numUnsentMessages())
	}
}

func (h *baseHandler) reportFlushError(generation uint64, err error) {
	if err == nil {
		return
	}
	h.ensureFlushChannels()
	h.Logger().Error(h.user, "Unable to flush session output", err)
	message := encodeGeneratedMessage(generation,
		fmt.Sprintf("Unable to flush session output: %v\n", err))
	select {
	case h.flushErrors <- message:
	default:
		h.Logger().Error(h.user, "Unable to queue flush error for client", err)
	}
}

func (h *baseHandler) requestCloseSync() {
	if !h.readerSeen.Load() {
		// Raw-channel consumers do not run Read and retain ownership of the
		// serverMessages protocol queue.
		go func() {
			defer recoverHandlerPanic(h.Logger(), h.user, "shutdown acknowledgement sender", h.abortAfterPanic)
			select {
			case h.serverMessages <- ".syn close connection":
			case <-h.done.Done():
			}
		}()
		return
	}

	h.ensureFlushChannels()
	h.closeSyncRequested.Store(true)
	select {
	case h.closeSyncWake <- struct{}{}:
	default:
	}
}

func (h *baseHandler) shutdown() {
	// Log current state at shutdown
	activeCommands := atomic.LoadInt32(&h.activeCommands)
	h.Logger().Info(h.user, "shutdown() called", "activeCommands", activeCommands, "outputMode", h.output.enabled())

	// In output mode, ensure all data is flushed before shutdown
	if h.output.enabled() {
		if err := h.flushOutput(context.Background()); err != nil {
			h.reportFlushError(0, fmt.Errorf("flush direct output: %w", err))
		}
	}

	// Shutdown the aggregate BEFORE flush to ensure MapReduce data is available.
	// Use the atomic accessor to avoid a data race with handleMapCommand which
	// may be concurrently storing the aggregate pointer on another goroutine.
	if ta := h.getAggregate(); ta != nil {
		h.Logger().Info(h.user, "Shutting down output aggregate in shutdown()")
		ta.Shutdown()
	}

	if err := h.flush(); err != nil {
		h.reportFlushError(0, err)
	}

	h.requestCloseSync()

	select {
	case <-h.ackCloseReceived:
	case <-time.After(time.Second * 5):
		h.Logger().Debug(h.user, "Shutdown timeout reached, enforcing shutdown")
	case <-h.done.Done():
	}
	h.done.Shutdown()
}

func (h *baseHandler) beginCommand(admittedSessionWork bool) bool {
	h.commandMu.Lock()
	defer h.commandMu.Unlock()
	if h.aborting || (h.stopping && !admittedSessionWork) {
		return false
	}
	h.commandWg.Add(1)
	h.commandInitWg.Add(1)
	atomic.AddInt32(&h.activeCommands, 1)
	return true
}

func (h *baseHandler) finishCommandInitialization() {
	h.commandInitWg.Done()
}

func (h *baseHandler) decrementActiveCommands() int32 {
	atomic.AddInt32(&h.activeCommands, -1)
	return atomic.LoadInt32(&h.activeCommands)
}

func (h *baseHandler) finishCommand() {
	h.commandWg.Done()
}

func (h *baseHandler) isStopping() bool {
	h.commandMu.Lock()
	defer h.commandMu.Unlock()
	return h.stopping
}

// stopCommandAdmission prevents WaitGroup additions before graceful shutdown
// waits for every already-admitted command to finish synchronous setup.
func (h *baseHandler) stopCommandAdmission() {
	h.commandMu.Lock()
	h.stopping = true
	h.commandMu.Unlock()
}

func (h *baseHandler) cancelCommandWork() {
	h.commandMu.Lock()
	if h.commandDone != nil {
		h.commandDone.Shutdown()
	}
	h.commandMu.Unlock()
}

// abortAfterPanic signals every session-owned producer and consumer to stop.
// It deliberately does not wait: panic recovery often runs from a command
// goroutine which is itself included in commandWg.
func (h *baseHandler) abortAfterPanic() {
	h.commandMu.Lock()
	h.stopping = true
	h.aborting = true
	if h.outputAbort != nil {
		h.outputAbort.Shutdown()
	}
	if h.commandDone != nil {
		h.commandDone.Shutdown()
	}
	aggregate := h.getAggregate()
	h.commandMu.Unlock()
	if aggregate != nil {
		aggregate.Abort()
	}
	if h.done != nil {
		h.done.Shutdown()
	}
}

func (h *baseHandler) outputAbortDone() <-chan struct{} {
	if h.outputAbort == nil {
		return nil
	}
	return h.outputAbort.Done()
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

// flushOutput waits until all output data has been consumed by the session reader.
func (h *baseHandler) flushOutput(ctx context.Context) error {
	if err := h.output.flush(ctx, h.user); err != nil {
		return err
	}
	if !h.readActive.Load() && !h.deliveryPending.Load() {
		return nil
	}
	return h.flushContext(ctx)
}

// EnqueueOutput adds generated payload to the byte-bounded session output buffer.
func (h *baseHandler) EnqueueOutput(ctx context.Context, generation uint64, payload []byte,
	activeGeneration func() uint64) error {
	return h.output.enqueue(ctx, generation, payload, activeGeneration)
}

// OutputBufferBytes returns the number of payload bytes awaiting delivery.
func (h *baseHandler) OutputBufferBytes() int {
	return h.output.bufferedLen()
}

// WaitForOutputEOFAck waits until output reader acknowledges EOF, cancellation,
// or timeout.
func (h *baseHandler) WaitForOutputEOFAck(ctx context.Context, timeout time.Duration) bool {
	return h.output.waitForEOFAck(ctx, timeout)
}
