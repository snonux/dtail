package handlers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/protocol"
)

type baseHandler struct {
	done         *internal.Done
	server       string
	shellStarted bool
	commands     chan string
	receiveBuf   bytes.Buffer
	status       int

	capabilitiesMu sync.RWMutex
	capabilities   map[string]struct{}
	capabilitiesCh chan struct{}
	capabilitiesOk sync.Once

	sessionAcks chan SessionAck
}

// SessionAck is a parsed hidden acknowledgement for SESSION START/UPDATE requests.
type SessionAck struct {
	Action     string
	Generation uint64
	Error      string
}

func (h *baseHandler) String() string {
	return fmt.Sprintf("baseHandler(%s,server:%s,shellStarted:%v,status:%d)@%p",
		h.done,
		h.server,
		h.shellStarted,
		h.status,
		h,
	)
}

func (h *baseHandler) Server() string {
	return h.server
}

func (h *baseHandler) Status() int {
	return h.status
}

func (h *baseHandler) Capabilities() []string {
	h.capabilitiesMu.RLock()
	defer h.capabilitiesMu.RUnlock()

	capabilities := make([]string, 0, len(h.capabilities))
	for capability := range h.capabilities {
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)
	return capabilities
}

func (h *baseHandler) HasCapability(name string) bool {
	h.capabilitiesMu.RLock()
	defer h.capabilitiesMu.RUnlock()

	_, ok := h.capabilities[name]
	return ok
}

// SendMessage to the server.
func (h *baseHandler) SendMessage(command string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	dlog.Client.Debug("Sending command", h.server, command, encoded)

	select {
	case h.commands <- fmt.Sprintf("protocol %s base64 %v;", protocol.ProtocolCompat, encoded):
	case <-time.After(time.Second * 5):
		return fmt.Errorf("Timed out sending command '%s' (base64: '%s')", command, encoded)
	case <-h.Done():
		return nil
	}

	return nil
}

// Read data from the dtail server via Writer interface.
func (h *baseHandler) Write(p []byte) (n int, err error) {
	for _, b := range p {
		switch b {
		case '\n':
			// Just add the newline to the buffer, don't treat as message delimiter
			h.receiveBuf.WriteByte(b)
		case protocol.MessageDelimiter:
			message := h.receiveBuf.String()
			h.handleMessage(message)
			h.receiveBuf.Reset()
		default:
			h.receiveBuf.WriteByte(b)
		}
	}
	return len(p), nil
}

// Send data to the dtail server via Reader interface.
//
// Priority select: when Done() is closed we must still drain any pending
// commands before returning io.EOF, because closing the connection requires
// the '.ack close connection' message to be flushed to the server first.
// Without this drain the server waits up to 5 seconds for the ack.
func (h *baseHandler) Read(p []byte) (n int, err error) {
	// Check for a pending command first (non-blocking), giving it priority
	// over the Done signal so that queued acks are always delivered.
	select {
	case command := <-h.commands:
		n = copy(p, []byte(command))
		return
	default:
	}

	// No command is immediately ready; block on whichever arrives first.
	select {
	case command := <-h.commands:
		n = copy(p, []byte(command))
	case <-h.Done():
		return 0, io.EOF
	}
	return
}

func (h *baseHandler) handleMessage(message string) {
	if len(message) > 0 && message[0] == '.' {
		h.handleHiddenMessage(message)
		return
	}
	if h.handleAuthKeyMessage(message) {
		return
	}

	// Add newline only if the message doesn't already end with one
	if len(message) > 0 && message[len(message)-1] == '\n' {
		dlog.Client.Raw(message)
	} else {
		dlog.Client.Raw(message + "\n")
	}
}

func (h *baseHandler) handleAuthKeyMessage(message string) bool {
	isAuthKeyMessage, authKeyOK, authKeyDetail := parseAuthKeyMessage(message)
	if !isAuthKeyMessage {
		return false
	}

	if authKeyOK {
		dlog.Client.Debug(h.server, "AUTHKEY registration accepted by server")
		return true
	}

	if authKeyDetail == "" {
		dlog.Client.Warn(h.server, "AUTHKEY registration failed")
		return true
	}

	dlog.Client.Warn(h.server, "AUTHKEY registration failed", authKeyDetail)
	return true
}

func parseAuthKeyMessage(message string) (isAuthKeyMessage bool, ok bool, detail string) {
	if message == "" {
		return false, false, ""
	}

	payload := strings.TrimSpace(message)
	parts := strings.Split(payload, protocol.FieldDelimiter)
	if len(parts) > 0 {
		payload = strings.TrimSpace(parts[len(parts)-1])
	}

	switch {
	case payload == "AUTHKEY OK":
		return true, true, ""
	case strings.HasPrefix(payload, "AUTHKEY ERR"):
		detail := strings.TrimSpace(strings.TrimPrefix(payload, "AUTHKEY ERR"))
		return true, false, detail
	default:
		return false, false, ""
	}
}

// Handle messages received from server which are not meant to be displayed
// to the end user.
func (h *baseHandler) handleHiddenMessage(message string) {
	switch {
	case strings.HasPrefix(message, protocol.HiddenCapabilitiesPrefix):
		h.handleCapabilitiesMessage(message)
	case strings.HasPrefix(message, protocol.HiddenSessionStartOKPrefix),
		strings.HasPrefix(message, protocol.HiddenSessionUpdateOKPrefix),
		strings.HasPrefix(message, protocol.HiddenSessionErrorPrefix):
		h.handleSessionAckMessage(message)
	case strings.HasPrefix(message, ".syn close connection"):
		if err := h.SendMessage(".ack close connection"); err != nil {
			dlog.Client.Debug(h.server, "Unable to acknowledge close connection", err)
		}
		h.Shutdown()
	}
}

func (h *baseHandler) handleCapabilitiesMessage(message string) {
	capabilities := strings.Fields(strings.TrimPrefix(message, protocol.HiddenCapabilitiesPrefix))

	h.capabilitiesMu.Lock()
	defer h.capabilitiesMu.Unlock()

	if h.capabilities == nil {
		h.capabilities = make(map[string]struct{})
	}
	for _, capability := range capabilities {
		if capability == "" {
			continue
		}
		h.capabilities[capability] = struct{}{}
	}

	h.capabilitiesOk.Do(func() {
		if h.capabilitiesCh != nil {
			close(h.capabilitiesCh)
		}
	})
}

func (h *baseHandler) Done() <-chan struct{} {
	return h.done.Done()
}

func (h *baseHandler) WaitForCapabilities(timeout time.Duration) bool {
	if h.capabilitiesCh == nil {
		return false
	}

	if timeout <= 0 {
		select {
		case <-h.capabilitiesCh:
			return true
		default:
			return false
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-h.capabilitiesCh:
		return true
	case <-h.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (h *baseHandler) WaitForSessionAck(timeout time.Duration) (SessionAck, bool) {
	if h.sessionAcks == nil {
		return SessionAck{}, false
	}

	if timeout <= 0 {
		select {
		case ack := <-h.sessionAcks:
			return ack, true
		default:
			return SessionAck{}, false
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case ack := <-h.sessionAcks:
		return ack, true
	case <-h.Done():
		return SessionAck{}, false
	case <-timer.C:
		return SessionAck{}, false
	}
}

func (h *baseHandler) Shutdown() {
	h.done.Shutdown()
}

func (h *baseHandler) handleSessionAckMessage(message string) {
	ack, ok := parseSessionAckMessage(message)
	if !ok {
		dlog.Client.Warn(h.server, "Unable to parse session acknowledgement", message)
		return
	}
	if h.sessionAcks == nil {
		return
	}

	select {
	case h.sessionAcks <- ack:
	case <-h.Done():
	default:
		dlog.Client.Warn(h.server, "Dropping session acknowledgement because the queue is full", message)
	}
}

func parseSessionAckMessage(message string) (SessionAck, bool) {
	payload := strings.TrimSpace(message)
	if payload == "" {
		return SessionAck{}, false
	}

	switch {
	case strings.HasPrefix(payload, protocol.HiddenSessionStartOKPrefix):
		return parseSessionOKAck(strings.TrimPrefix(payload, protocol.HiddenSessionStartOKPrefix), "start")
	case strings.HasPrefix(payload, protocol.HiddenSessionUpdateOKPrefix):
		return parseSessionOKAck(strings.TrimPrefix(payload, protocol.HiddenSessionUpdateOKPrefix), "update")
	case strings.HasPrefix(payload, protocol.HiddenSessionErrorPrefix):
		return SessionAck{
			Action: "error",
			Error:  strings.TrimSpace(strings.TrimPrefix(payload, protocol.HiddenSessionErrorPrefix)),
		}, true
	default:
		return SessionAck{}, false
	}
}

func parseSessionOKAck(payload string, action string) (SessionAck, bool) {
	generationStr := strings.TrimSpace(payload)
	if generationStr == "" {
		return SessionAck{}, false
	}

	generation, err := strconv.ParseUint(generationStr, 10, 64)
	if err != nil {
		return SessionAck{}, false
	}

	return SessionAck{
		Action:     action,
		Generation: generation,
	}, true
}
