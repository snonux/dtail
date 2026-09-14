package handlers

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/logging"
	mapaggregate "github.com/mimecast/dtail/internal/mapr/aggregate"
	user "github.com/mimecast/dtail/internal/sessionuser"
)

type baseHandlerConfig struct {
	logger              logging.Logger
	readerLogger        logging.Logger
	serverlessOutput    io.Writer
	user                *user.User
	serverMessages      chan string
	maprMessages        chan string
	maxCommandFrameSize int
	serverless          bool
	hostname            string
	activeGeneration    func() uint64
}

type baseHandler struct {
	logger           logging.Logger
	readerLogger     logging.Logger
	serverlessOutput io.Writer
	done             *internal.Done
	user             *user.User

	// aggregate is written by handleMapCommand on the command-dispatch
	// goroutine and read concurrently by Shutdown, Aggregate, and
	// resetSessionAggregates. Using atomic.Pointer eliminates the data race.
	aggregate atomic.Pointer[mapaggregate.Aggregate]

	*sessionFramer
	*commandDispatcher
	*outputCoordinator
}

// newBaseHandler builds the three focused session components and binds them to
// one stable handler pointer. Keeping this wiring in one place prevents partial
// handlers whose components disagree about the session they serve.
func newBaseHandler(cfg baseHandlerConfig) *baseHandler {
	serverMessages := cfg.serverMessages
	if serverMessages == nil {
		serverMessages = make(chan string, 10)
	}
	maprMessages := cfg.maprMessages
	if maprMessages == nil {
		maprMessages = make(chan string, 10)
	}

	h := &baseHandler{
		logger:           logging.OrNop(cfg.logger),
		readerLogger:     logging.OrNop(cfg.readerLogger),
		serverlessOutput: cfg.serverlessOutput,
		done:             internal.NewDone(),
		user:             cfg.user,
	}
	h.sessionFramer = &sessionFramer{
		handler:             h,
		maxCommandFrameSize: cfg.maxCommandFrameSize,
	}
	h.commandDispatcher = &commandDispatcher{
		handler:     h,
		codec:       newProtocolCodec(cfg.user, cfg.logger),
		commandDone: internal.NewDone(),
		serverless:  cfg.serverless,
	}
	h.outputCoordinator = &outputCoordinator{
		handler:          h,
		maprMessages:     maprMessages,
		serverMessages:   serverMessages,
		hostname:         cfg.hostname,
		ackCloseReceived: make(chan struct{}),
		outputAbort:      internal.NewDone(),
		activeGeneration: cfg.activeGeneration,
	}
	return h
}

// Logger returns the handler logger, falling back to a no-op logger for
// focused unit tests that do not need diagnostics.
func (h *baseHandler) Logger() logging.Logger {
	return logging.OrNop(h.logger)
}

// ReaderLogger returns the logger used by filesystem reader dependencies.
func (h *baseHandler) ReaderLogger() logging.Logger {
	return logging.OrNop(h.readerLogger)
}

// Shutdown aborts all session work and waits for admitted commands to stop.
func (h *baseHandler) Shutdown() {
	h.commandMu.Lock()
	h.stopping = true
	h.aborting = true
	h.outputAbort.Shutdown()
	if aggregate := h.getAggregate(); aggregate != nil {
		aggregate.Abort()
	}
	h.commandDone.Shutdown()
	h.commandMu.Unlock()

	if aggregate := h.getAggregate(); aggregate != nil {
		h.Logger().Info(h.user, "Aborting output aggregate")
		aggregate.AbortAndWait()
	}
	h.done.Shutdown()
	h.commandWg.Wait()
}

// Done returns the handler shutdown channel.
func (h *baseHandler) Done() <-chan struct{} {
	return h.done.Done()
}

// getAggregate returns the current output MapReduce aggregate atomically.
func (h *baseHandler) getAggregate() *mapaggregate.Aggregate {
	return h.aggregate.Load()
}

// setAggregate stores an output MapReduce aggregate atomically.
func (h *baseHandler) setAggregate(aggregate *mapaggregate.Aggregate) {
	h.aggregate.Store(aggregate)
}

// shutdown gracefully drains output, performs the close acknowledgement
// handshake, and then closes the session.
func (h *baseHandler) shutdown() {
	activeCommands := atomic.LoadInt32(&h.activeCommands)
	h.Logger().Info(h.user, "shutdown() called", "activeCommands", activeCommands,
		"outputMode", h.output.enabled())

	if h.output.enabled() {
		if err := h.flushOutput(context.Background()); err != nil {
			h.reportFlushError(0, fmt.Errorf("flush direct output: %w", err))
		}
	}

	if aggregate := h.getAggregate(); aggregate != nil {
		h.Logger().Info(h.user, "Shutting down output aggregate in shutdown()")
		aggregate.Shutdown()
	}

	if err := h.flush(); err != nil {
		h.reportFlushError(0, err)
	}

	h.requestCloseSync()
	h.waitForCloseAcknowledgement()
	h.done.Shutdown()
}

// abortAfterPanic signals every session-owned producer and consumer to stop.
// It deliberately does not wait because recovery can run inside commandWg.
func (h *baseHandler) abortAfterPanic() {
	h.commandMu.Lock()
	h.stopping = true
	h.aborting = true
	h.outputAbort.Shutdown()
	h.commandDone.Shutdown()
	aggregate := h.getAggregate()
	h.commandMu.Unlock()
	if aggregate != nil {
		aggregate.Abort()
	}
	h.done.Shutdown()
}

var _ Handler = (*baseHandler)(nil)
