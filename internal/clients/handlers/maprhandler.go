package handlers

import (
	"strings"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr/client"
	"github.com/mimecast/dtail/internal/protocol"
)

// aggregateMessagePrefix is the leading part of a mapreduce aggregate-data
// wire message (AGGREGATE|host|data). Classifying incoming messages against
// this full prefix, rather than just their first byte, prevents plain-mode
// protocol acks (e.g. "AUTHKEY OK") from being fed to the aggregate parser.
const aggregateMessagePrefix = protocol.AggregateMessageID + protocol.FieldDelimiter

// MaprHandler is the handler used on the client side for running mapreduce
// aggregations.
type MaprHandler struct {
	baseHandler
	aggregate *client.Aggregate
	removedNl bool
}

// NewMaprHandler returns a new mapreduce client handler.
func NewMaprHandler(server string, session *client.SessionState) *MaprHandler {

	return &MaprHandler{
		baseHandler: baseHandler{
			server:         server,
			shellStarted:   false,
			commands:       make(chan string),
			status:         -1,
			done:           internal.NewDone(),
			capabilities:   make(map[string]struct{}),
			capabilitiesCh: make(chan struct{}),
			sessionAcks:    make(chan SessionAck, 4),
		},
		aggregate: client.NewAggregate(server, session),
	}
}

// Read data from the dtail server via Writer interface.
func (h *MaprHandler) Write(p []byte) (n int, err error) {
	for _, b := range p {
		switch b {
		case '\n':
			h.removedNl = true
		case protocol.MessageDelimiter:
			message := h.baseHandler.receiveBuf.String()
			if len(message) == 0 {
				h.baseHandler.receiveBuf.Reset()
				h.removedNl = false
				continue
			}
			dlog.Client.Debug(message)
			if isAggregateMessage(message) {
				h.handleAggregateMessage(message)
			} else {
				if h.removedNl {
					h.baseHandler.handleMessage(message + "\n")
				} else {
					h.baseHandler.handleMessage(message)
				}
			}
			h.baseHandler.receiveBuf.Reset()
			h.removedNl = false
		default:
			h.baseHandler.receiveBuf.WriteByte(b)
		}
	}

	return len(p), nil
}

// isAggregateMessage reports whether a wire message carries mapreduce
// aggregate data (AGGREGATE|host|data). Only such messages may be handed to
// the aggregate parser. Matching the full AggregateMessageID field prefix,
// rather than just the first byte 'A', keeps plain-mode protocol acks such as
// "AUTHKEY OK" out of the parser; those would otherwise trigger a spurious
// "Unable to aggregate data ... expected 3 parts" error. Non-aggregate
// messages are routed to the base handler, which recognises acks as control.
func isAggregateMessage(message string) bool {
	return strings.HasPrefix(message, aggregateMessagePrefix)
}

// Handle a message received from server including mapr aggregation related data.
func (h *MaprHandler) handleAggregateMessage(message string) {
	parts := strings.SplitN(message, protocol.FieldDelimiter, 3)
	if len(parts) != 3 {
		dlog.Client.Error("Unable to aggregate data", h.server, message, parts,
			len(parts), "expected 3 parts")
		return
	}
	if err := h.aggregate.Aggregate(parts[2]); err != nil {
		dlog.Client.Error("Unable to aggregate data", h.server, message, err)
	}
}

// Shutdown flushes any pending aggregate state before marking the handler done.
func (h *MaprHandler) Shutdown() {
	if h.aggregate != nil {
		if err := h.aggregate.Flush(); err != nil {
			dlog.Client.Error("Unable to flush aggregate data on shutdown", h.server, err)
		}
	}
	h.baseHandler.Shutdown()
}
