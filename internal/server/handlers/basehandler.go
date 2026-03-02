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

	if n, handled := h.turbo.tryRead(p, h.user); handled {
		return n, nil
	}

	select {
	case message := <-h.serverMessages:
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

	case message := <-h.maprMessages:
		// Send mapreduce-aggregated data as a message.
		h.readBuf.WriteString("AGGREGATE")
		h.readBuf.WriteString(protocol.FieldDelimiter)
		h.readBuf.WriteString(h.hostname)
		h.readBuf.WriteString(protocol.FieldDelimiter)
		h.readBuf.WriteString(message)
		h.readBuf.WriteByte(protocol.MessageDelimiter)
		n = copy(p, h.readBuf.Bytes())

	case line := <-h.lines:
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

	case <-time.After(time.Second):
		select {
		case <-h.done.Done():
			err = io.EOF
			return
		default:
		}
	}
	return
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
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-h.done.Done()
		cancel()
	}()

	parts := strings.Split(args[0], ":")
	commandName := parts[0]

	// Either no options or empty options provided.
	if len(parts) == 1 || len(parts[1]) == 0 {
		h.handleCommandCb(ctx, lcontext.LContext{}, argc, args, commandName)
		return
	}

	options, ltx, err := config.DeserializeOptions(parts[1:])
	if err != nil {
		h.sendln(h.serverMessages, dlog.Server.Error(h.user, err))
		return
	}
	h.handleOptions(options)
	h.handleCommandCb(ctx, ltx, argc, args, commandName)
}

func (h *baseHandler) handleProtocolVersion(args []string) ([]string, int, string, error) {
	return h.codec.handleProtocolVersion(args)
}

func (h *baseHandler) handleBase64(args []string, argc int) ([]string, int, error) {
	return h.codec.handleBase64(args, argc)
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

	// Increase iterations for turbo mode to handle large file batches
	maxIterations := 100
	if h.turbo.enabled() {
		maxIterations = 300 // Give more time for turbo mode to drain
	}
	// Also increase iterations if we have MapReduce messages
	if h.turboAggregate != nil || h.aggregate != nil {
		maxIterations = 300 // Give more time for MapReduce results
	}

	for i := 0; i < maxIterations; i++ {
		if numUnsentMessages() == 0 {
			dlog.Server.Debug(h.user, "ALL lines sent", fmt.Sprintf("%p", h))
			return
		}
		dlog.Server.Debug(h.user, "Still lines to be sent", "iteration", i, "unsent", numUnsentMessages())
		time.Sleep(time.Millisecond * 10)
	}
	dlog.Server.Warn(h.user, "Some lines remain unsent", numUnsentMessages())
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
