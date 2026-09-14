package handlers

import (
	"bytes"
	"io"

	"github.com/mimecast/dtail/internal/protocol"
)

// sessionFramer owns byte-stream boundaries in both directions. The SSH
// transport presents arbitrary chunks, so command frames and output messages
// must retain partial bytes across calls.
type sessionFramer struct {
	handler  *baseHandler
	readBuf  bytes.Buffer
	writeBuf bytes.Buffer

	maxCommandFrameSize int
}

// Read emits one or more bytes from the next coordinated output item.
func (f *sessionFramer) Read(p []byte) (n int, err error) {
	c := f.handler.outputCoordinator
	c.ensureFlushChannels()
	c.readerSeen.Store(true)
	c.readActive.Store(true)
	c.deliveryPending.Store(false)
	defer func() {
		if n > 0 {
			c.deliveryPending.Store(true)
		}
		c.readActive.Store(false)
	}()

	if f.readBuf.Len() > 0 {
		return f.drainReadBuf(p), nil
	}

	for {
		result := c.next(p)
		if result.err != nil {
			return 0, result.err
		}
		switch result.kind {
		case outputReadBytes:
			return result.n, nil
		case outputReadServerMessage:
			n = f.readServerMessage(p, result.message)
		case outputReadMaprMessage:
			n = f.readMaprMessage(p, result.message)
		case outputReadRetry:
			continue
		}
		if n > 0 {
			return n, nil
		}
	}
}

// Write accumulates command bytes until a semicolon delimiter is received.
func (f *sessionFramer) Write(p []byte) (n int, err error) {
	h := f.handler
	for _, b := range p {
		switch b {
		case ';':
			h.handleCommand(f.writeBuf.String())
			f.writeBuf.Reset()
		default:
			f.writeBuf.WriteByte(b)
			if f.maxCommandFrameSize > 0 && f.writeBuf.Len() > f.maxCommandFrameSize {
				h.Logger().Error(h.user,
					"command frame exceeds maximum size, closing session",
					"frameSize", f.writeBuf.Len(),
					"limit", f.maxCommandFrameSize,
				)
				f.writeBuf.Reset()
				h.done.Shutdown()
				return len(p), io.ErrClosedPipe
			}
		}
	}
	return len(p), nil
}

func (f *sessionFramer) readServerMessage(p []byte, message string) int {
	h := f.handler
	generation, decodedMessage := decodeGeneratedMessage(message)
	if h.shouldDropGeneration(generation) {
		return 0
	}
	message = decodedMessage
	if len(message) > 0 && message[0] == '.' {
		f.readBuf.WriteString(message)
		f.readBuf.WriteByte(protocol.MessageDelimiter)
		return f.drainReadBuf(p)
	}
	if h.serverless || h.plain && (message == "" || message == "\n") {
		return 0
	}
	formatServerMessage(&f.readBuf, h.hostname, message, h.plain)
	return f.drainReadBuf(p)
}

func (f *sessionFramer) readMaprMessage(p []byte, message string) int {
	h := f.handler
	generation, decodedMessage := decodeGeneratedMessage(message)
	if h.shouldDropGeneration(generation) {
		return 0
	}
	f.readBuf.WriteString(protocol.AggregateMessageID)
	f.readBuf.WriteString(protocol.FieldDelimiter)
	f.readBuf.WriteString(h.hostname)
	f.readBuf.WriteString(protocol.FieldDelimiter)
	f.readBuf.WriteString(decodedMessage)
	f.readBuf.WriteByte(protocol.MessageDelimiter)
	return f.drainReadBuf(p)
}

func (f *sessionFramer) drainReadBuf(p []byte) int {
	n, _ := f.readBuf.Read(p)
	f.handler.readBufferedBytes.Store(int64(f.readBuf.Len()))
	return n
}

var _ io.ReadWriter = (*sessionFramer)(nil)
