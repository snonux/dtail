package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	user "github.com/mimecast/dtail/internal/user/server"
)

const (
	commandParseWarning string = "Unable to parse command"
)

// ServerHandler implements the Reader and Writer interfaces to handle
// the Bi-directional communication between SSH client and server.
// This handler implements the handler of the SSH server.
type ServerHandler struct {
	done             *internal.Done
	lines            chan line.Line
	regex            string
	aggregate        *server.Aggregate
	maprMessages     chan string
	serverMessages   chan string
	hostname         string
	user             *user.User
	catLimiter       chan struct{}
	tailLimiter      chan struct{}
	ackCloseReceived chan struct{}
	activeCommands   int32
	quiet            bool
	spartan          bool
	serverless       bool
	readBuf          bytes.Buffer
	writeBuf         bytes.Buffer
}

// NewServerHandler returns the server handler.
func NewServerHandler(user *user.User, catLimiter, tailLimiter chan struct{}) *ServerHandler {
	h := ServerHandler{
		done:             internal.NewDone(),
		lines:            make(chan line.Line, 100),
		serverMessages:   make(chan string, 10),
		maprMessages:     make(chan string, 10),
		ackCloseReceived: make(chan struct{}),
		catLimiter:       catLimiter,
		tailLimiter:      tailLimiter,
		regex:            ".",
		user:             user,
	}

	fqdn, err := os.Hostname()
	if err != nil {
		dlog.Server.FatalPanic(err)
	}

	s := strings.Split(fqdn, ".")
	h.hostname = s[0]

	return &h
}

// Shutdown the handler.
func (h *ServerHandler) Shutdown() {
	h.done.Shutdown()
}

// Done channel of the handler.
func (h *ServerHandler) Done() <-chan struct{} {
	return h.done.Done()
}

// Read is to send data to the dtail client via Reader interface.
func (h *ServerHandler) Read(p []byte) (n int, err error) {
	defer h.readBuf.Reset()

	select {
	case message := <-h.serverMessages:
		if message[0] == '.' {
			// Handle hidden message (don't display to the user, interpreted by dtail client)
			h.readBuf.WriteString(message)
			h.readBuf.WriteByte(protocol.MessageDelimiter)
			n = copy(p, h.readBuf.Bytes())
			return
		}

		if h.serverless {
			// In serverless mode we have logged the server message already via the
			// dlog logger, no need to send the message again to the client part.
			return
		}

		// Handle normal server message (display to the user)
		h.readBuf.WriteString("SERVER")
		h.readBuf.WriteString(protocol.FieldDelimiter)
		h.readBuf.WriteString(h.hostname)
		h.readBuf.WriteString(protocol.FieldDelimiter)
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
		if !h.spartan {
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

	case <-time.After(time.Second):
		// Once in a while check whether we are done.
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
func (h *ServerHandler) Write(p []byte) (n int, err error) {
	for _, b := range p {
		switch b {
		case ';':
			h.handleCommand(string(h.writeBuf.Bytes()))
			h.writeBuf.Reset()
		default:
			h.writeBuf.WriteByte(b)
		}
	}

	n = len(p)
	return
}

func (h *ServerHandler) handleCommand(commandStr string) {
	dlog.Server.Debug(h.user, commandStr)
	ctx := context.Background()

	args, argc, add, err := h.handleProtocolVersion(strings.Split(commandStr, " "))
	if err != nil {
		h.send(h.serverMessages, dlog.Server.Error(h.user, err)+add)
		return
	}

	args, argc, err = h.handleBase64(args, argc)
	if err != nil {
		h.send(h.serverMessages, dlog.Server.Error(h.user, err))
		return
	}

	if h.user.Name == config.ControlUser {
		h.handleControlCommand(argc, args)
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	go func() {
		<-h.done.Done()
		cancel()
	}()

	h.handleUserCommand(ctx, argc, args)
}

func (h *ServerHandler) handleProtocolVersion(args []string) ([]string, int, string, error) {
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

		err := fmt.Errorf("DTail server protocol version '%s' does not match client protocol version '%s', please update DTail %s!",
			protocol.ProtocolCompat, args[1], toUpdate)
		return args, argc, add, err
	}

	return args[2:], argc - 2, add, nil
}

func (h *ServerHandler) handleBase64(args []string, argc int) ([]string, int, error) {
	err := errors.New("Unable to decode client message, DTail server and client versions may not be compatible")

	if argc != 2 || args[0] != "base64" {
		return args, argc, err
	}

	decoded, err := base64.StdEncoding.DecodeString(args[1])
	if err != nil {
		return args, argc, err
	}
	decodedStr := string(decoded)

	args = strings.Split(decodedStr, " ")
	argc = len(decodedStr)
	dlog.Server.Trace(h.user, "Base64 decoded received command", decodedStr, argc, args)

	return args, argc, nil
}

func (h *ServerHandler) handleControlCommand(argc int, args []string) {
	switch args[0] {
	case "debug":
		h.send(h.serverMessages, dlog.Server.Debug(h.user, "Receiving debug command", argc, args))
	default:
		dlog.Server.Warn(h.user, "Received unknown control command", argc, args)
	}
}

func (h *ServerHandler) handleUserCommand(ctx context.Context, argc int, args []string) {
	dlog.Server.Debug(h.user, "handleUserCommand", argc, args)

	h.incrementActiveCommands()
	commandFinished := func() {
		if h.decrementActiveCommands() == 0 {
			h.shutdown()
		}
	}

	splitted := strings.Split(args[0], ":")
	commandName := splitted[0]

	options, err := config.DeserializeOptions(splitted[1:])
	if err != nil {
		h.send(h.serverMessages, dlog.Server.Error(h.user, err))
		commandFinished()
		return
	}

	if quiet, _ := options["quiet"]; quiet == "true" {
		dlog.Server.Debug(h.user, "Enabling quiet mode")
		h.quiet = true
	}
	if spartan, _ := options["spartan"]; spartan == "true" {
		dlog.Server.Debug(h.user, "Enabling spartan mode")
		h.spartan = true
	}
	if serverless, _ := options["serverless"]; serverless == "true" {
		dlog.Server.Debug(h.user, "Enabling serverless mode")
		h.serverless = true
	}

	switch commandName {
	case "grep", "cat":
		command := newReadCommand(h, omode.CatClient)
		go func() {
			command.Start(ctx, argc, args, 1)
			commandFinished()
		}()

	case "tail":
		command := newReadCommand(h, omode.TailClient)
		go func() {
			command.Start(ctx, argc, args, 10)
			commandFinished()
		}()

	case "map":
		command, aggregate, err := newMapCommand(h, argc, args)
		if err != nil {
			h.send(h.serverMessages, err.Error())
			dlog.Server.Error(h.user, err)
			commandFinished()
			return
		}

		h.aggregate = aggregate
		go func() {
			command.Start(ctx, h.maprMessages)
			commandFinished()
		}()

	case "ack", ".ack":
		h.handleAckCommand(argc, args)
		commandFinished()

	default:
		h.send(h.serverMessages, dlog.Server.Error(h.user, "Received unknown user command", commandName, argc, args, options))
		commandFinished()
	}
}

func (h *ServerHandler) handleAckCommand(argc int, args []string) {
	if argc < 3 {
		if !h.quiet {
			h.send(h.serverMessages, dlog.Server.Warn(h.user, commandParseWarning, args, argc))
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

func (h *ServerHandler) send(ch chan<- string, message string) {
	select {
	case ch <- message:
	case <-h.done.Done():
	}
}

func (h *ServerHandler) flush() {
	dlog.Server.Debug(h.user, "flush()")

	unsentMessages := func() int {
		return len(h.lines) + len(h.serverMessages) + len(h.maprMessages)
	}
	for i := 0; i < 3; i++ {
		if unsentMessages() == 0 {
			dlog.Server.Debug(h.user, "All lines sent")
			return
		}
		dlog.Server.Debug(h.user, "Still lines to be sent")
		time.Sleep(time.Second)
	}

	dlog.Server.Warn(h.user, "Some lines remain unsent", unsentMessages())
}

func (h *ServerHandler) shutdown() {
	dlog.Server.Debug(h.user, "shutdown()")
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

func (h *ServerHandler) incrementActiveCommands() {
	atomic.AddInt32(&h.activeCommands, 1)
}

func (h *ServerHandler) decrementActiveCommands() int32 {
	atomic.AddInt32(&h.activeCommands, -1)
	return atomic.LoadInt32(&h.activeCommands)
}
