package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
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
	turboAggregate   *server.TurboAggregate  // Turbo mode aggregate
	maprMessages     chan string
	serverMessages   chan string
	hostname         string
	user             *user.User
	ackCloseReceived chan struct{}
	activeCommands   int32
	readBuf          bytes.Buffer
	writeBuf         bytes.Buffer

	// Some global options + sync primitives required.
	once       sync.Once
	mutex      sync.Mutex
	quiet      bool
	plain      bool
	serverless bool
	
	// Turbo mode support
	turboMode    bool
	turboLines   chan []byte  // Pre-formatted lines for turbo mode
	turboBuffer  []byte       // Buffer for partially sent turbo data
	turboEOF     chan struct{} // Signal when turbo data is complete
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

	// In turbo mode, check if we have buffered data first
	if h.turboMode && len(h.turboBuffer) > 0 {
		dlog.Server.Trace(h.user, "baseHandler.Read", "using buffered turbo data", "bufferedLen", len(h.turboBuffer))
		n = copy(p, h.turboBuffer)
		h.turboBuffer = h.turboBuffer[n:]
		dlog.Server.Trace(h.user, "baseHandler.Read", "after buffer read", "copied", n, "remaining", len(h.turboBuffer))
		return
	}

	// In turbo mode, prioritize pre-formatted turbo lines
	if h.turboMode && h.turboLines != nil {
		channelLen := len(h.turboLines)
		dlog.Server.Trace(h.user, "baseHandler.Read", "checking turboLines channel", "channelLen", channelLen)
		
		// Try to read from the channel
		select {
		case turboData := <-h.turboLines:
			dlog.Server.Trace(h.user, "baseHandler.Read", "got data from turboLines", "dataLen", len(turboData))
			n = copy(p, turboData)
			// If we couldn't send all data, buffer the rest
			if n < len(turboData) {
				h.turboBuffer = turboData[n:]
				dlog.Server.Trace(h.user, "baseHandler.Read", "buffering remaining data", "bufferedLen", len(h.turboBuffer))
			}
			return
		default:
			// No data immediately available
			if channelLen > 0 {
				// There's data in the channel but we couldn't get it immediately
				// Wait a bit and try again
				dlog.Server.Trace(h.user, "baseHandler.Read", "channel has data but not available, waiting")
				time.Sleep(time.Millisecond)
				select {
				case turboData := <-h.turboLines:
					dlog.Server.Trace(h.user, "baseHandler.Read", "got data after wait", "dataLen", len(turboData))
					n = copy(p, turboData)
					if n < len(turboData) {
						h.turboBuffer = turboData[n:]
					}
					return
				default:
					// Still no data
				}
			}
			
			// Channel is truly empty, check if we should continue in turbo mode
			// Only disable turbo mode if we've been signaled to do so
			if h.turboEOF != nil {
				select {
				case <-h.turboEOF:
					dlog.Server.Trace(h.user, "baseHandler.Read", "EOF received and channel empty, disabling turbo mode")
					h.turboMode = false
				default:
					// EOF not signaled yet, continue in turbo mode
				}
			}
			
			dlog.Server.Trace(h.user, "baseHandler.Read", "no data in turboLines, falling through")
			// Fall through to normal processing
		}
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

		// Handle normal server message (display to the user)
		if !h.plain {
			h.readBuf.WriteString("SERVER")
			h.readBuf.WriteString(protocol.FieldDelimiter)
			h.readBuf.WriteString(h.hostname)
			h.readBuf.WriteString(protocol.FieldDelimiter)
		}
		h.readBuf.WriteString(message)
		h.readBuf.WriteByte(protocol.MessageDelimiter)
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
		if !h.plain {
			h.readBuf.WriteString("REMOTE")
			h.readBuf.WriteString(protocol.FieldDelimiter)
			h.readBuf.WriteString(h.hostname)
			h.readBuf.WriteString(protocol.FieldDelimiter)
			h.readBuf.WriteString(fmt.Sprintf("%3d", line.TransmittedPerc))
			h.readBuf.WriteString(protocol.FieldDelimiter)
			h.readBuf.WriteString(fmt.Sprintf("%v", line.Count))
			h.readBuf.WriteString(protocol.FieldDelimiter)
			h.readBuf.WriteString(line.SourceID)
			h.readBuf.WriteString(protocol.FieldDelimiter)
		}
		h.readBuf.WriteString(line.Content.String())
		h.readBuf.WriteByte(protocol.MessageDelimiter)
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
	argc := len(args)
	var add string

	if argc <= 2 || args[0] != "protocol" {
		return args, argc, add, errors.New("unable to determine protocol version")
	}

	if args[1] != protocol.ProtocolCompat {
		clientCompat, _ := strconv.Atoi(args[1])
		serverCompat, _ := strconv.Atoi(protocol.ProtocolCompat)
		if clientCompat <= 3 {
			// Protocol version 3 or lower expect a newline as message separator
			// One day (after 2 major versions) this exception may be removed!
			add = "\n"
		}

		toUpdate := "client"
		if clientCompat > serverCompat {
			toUpdate = "server"
		}
		err := fmt.Errorf("the DTail server protocol version '%s' does not match "+
			"client protocol version '%s', please update DTail %s",
			protocol.ProtocolCompat, args[1], toUpdate)
		return args, argc, add, err
	}

	return args[2:], argc - 2, add, nil
}

func (h *baseHandler) handleBase64(args []string, argc int) ([]string, int, error) {
	err := errors.New("unable to decode client message, DTail server and client " +
		"versions may not be compatible")
	if argc != 2 || args[0] != "base64" {
		return args, argc, err
	}

	decoded, err := base64.StdEncoding.DecodeString(args[1])
	if err != nil {
		return args, argc, err
	}
	decodedStr := string(decoded)

	args = strings.Split(decodedStr, " ")
	argc = len(args)
	dlog.Server.Trace(h.user, "Base64 decoded received command",
		decodedStr, argc, args)

	return args, argc, nil
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
		turboCount := 0
		if h.turboLines != nil {
			turboCount = len(h.turboLines)
		}
		dlog.Server.Trace(h.user, "flush", "lines", lineCount, "server", serverCount, "mapr", maprCount, "turbo", turboCount)
		return lineCount + serverCount + maprCount + turboCount
	}
	
	// Increase iterations for turbo mode to handle large file batches
	maxIterations := 100
	if h.turboMode {
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
	dlog.Server.Info(h.user, "shutdown() called", "activeCommands", activeCommands, "turboMode", h.turboMode)
	
	// In turbo mode, ensure all data is flushed before shutdown
	if h.turboMode {
		h.flushTurboData()
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
	h.turboMode = true
	if h.turboLines == nil {
		h.turboLines = make(chan []byte, 1000) // Large buffer for performance
	}
	// Always create a new turboEOF channel for each batch of files
	// This ensures proper synchronization when processing multiple file batches
	h.turboEOF = make(chan struct{})
}

// IsTurboMode returns true if turbo mode is enabled
func (h *baseHandler) IsTurboMode() bool {
	return h.turboMode
}

// flushTurboData ensures all turbo channel data is processed
func (h *baseHandler) flushTurboData() {
	if h.turboLines == nil {
		return
	}
	
	dlog.Server.Debug(h.user, "Flushing turbo data", "channelLen", len(h.turboLines))
	
	// Wait for turbo channel to drain with a timeout
	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-timeout:
			dlog.Server.Warn(h.user, "Timeout while flushing turbo data", "remaining", len(h.turboLines))
			return
		default:
			if len(h.turboLines) == 0 {
				dlog.Server.Debug(h.user, "Turbo channel drained successfully")
				return
			}
			// Give the reader time to process
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// GetTurboChannel returns the turbo lines channel for direct writing
func (h *baseHandler) GetTurboChannel() chan<- []byte {
	return h.turboLines
}
