package handlers

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mimecast/dtail/internal/protocol"
)

// sessionFramer owns byte-stream boundaries in both directions. The SSH
// transport presents arbitrary chunks, so command frames and output messages
// must retain partial bytes across calls.
//
// It also owns the session's output framing (see protocol.Framing). A session
// starts in RawFraming and switches to EscapedFraming at most once, when the
// client asks for it with the session's first command (enableEscapedFraming,
// handlingFirstCommand). The switch waits until the
// capabilities frame, the first frame of the session, has been encoded, so
// that frame is always raw and the client, which decodes every frame after
// it as escaped, agrees with the server on every frame.
type sessionFramer struct {
	handler  *baseHandler
	readBuf  bytes.Buffer
	writeBuf bytes.Buffer

	maxCommandFrameSize int
	// commandFrames counts the command frames Write passed on so far. It is
	// only touched by Write, i.e. by the command input goroutine.
	commandFrames int

	// escaped is set once the session switched to EscapedFraming. It is
	// written by the command input goroutine and read by the output
	// goroutine (Read) and by the command goroutines creating line writers.
	escaped atomic.Bool
	// capabilitiesFramed is closed once Read encoded the capabilities frame.
	capabilitiesFramed     chan struct{}
	capabilitiesFramedOnce sync.Once
}

func newSessionFramer(handler *baseHandler, maxCommandFrameSize int) *sessionFramer {
	return &sessionFramer{
		handler:             handler,
		maxCommandFrameSize: maxCommandFrameSize,
		capabilitiesFramed:  make(chan struct{}),
	}
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
			f.commandFrames++
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
				h.cancelCommands()
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
		f.framing().EncodeMessage(&f.readBuf, protocol.Message{
			Kind:    protocol.MessagePlain,
			Content: message,
		})
		if strings.HasPrefix(message, protocol.HiddenCapabilitiesPrefix) {
			f.capabilitiesFramedOnce.Do(func() { close(f.capabilitiesFramed) })
		}
		return f.drainReadBuf(p)
	}
	if h.serverless || isEmptyServerMessage(message) {
		return 0
	}
	kind := protocol.MessageServer
	if h.plain {
		kind = protocol.MessagePlain
	}
	f.framing().EncodeMessage(&f.readBuf, protocol.Message{
		Kind:     kind,
		Hostname: h.hostname,
		Content:  message,
	})
	return f.drainReadBuf(p)
}

func (f *sessionFramer) readMaprMessage(p []byte, message string) int {
	h := f.handler
	generation, decodedMessage := decodeGeneratedMessage(message)
	if h.shouldDropGeneration(generation) {
		return 0
	}
	f.framing().EncodeMessage(&f.readBuf, protocol.Message{
		Kind:     protocol.MessageAggregate,
		Hostname: h.hostname,
		Content:  decodedMessage,
	})
	return f.drainReadBuf(p)
}

// framing returns the framing of the frames the session encodes from now on.
func (f *sessionFramer) framing() protocol.Framing {
	if f.escaped.Load() {
		return protocol.EscapedFraming
	}
	return protocol.RawFraming
}

// handlingFirstCommand reports whether the command being handled is the
// session's first command frame. It is only meaningful while a command is
// handled on the command input goroutine (Write).
func (f *sessionFramer) handlingFirstCommand() bool {
	return f.commandFrames == 1
}

// enableEscapedFraming switches the session to EscapedFraming once the
// capabilities frame has been encoded. It runs on the command input
// goroutine, so no later command of the session is handled, and none of
// its frames encoded, before the switch. It gives up without switching when
// ctx or the session ends first. In practice it does not wait at all: the
// capabilities frame is queued when the session handler is built, before the
// client can send anything.
func (f *sessionFramer) enableEscapedFraming(ctx context.Context) bool {
	select {
	case <-f.capabilitiesFramed:
	case <-ctx.Done():
		return false
	case <-f.handler.done.Done():
		return false
	}
	f.escaped.Store(true)
	return true
}

func (f *sessionFramer) drainReadBuf(p []byte) int {
	n, _ := f.readBuf.Read(p)
	f.handler.readBufferedBytes.Store(int64(f.readBuf.Len()))
	return n
}

var _ io.ReadWriter = (*sessionFramer)(nil)
