package handlers

import (
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	user "github.com/mimecast/dtail/internal/user/server"
)

type turboManager struct {
	mode   bool
	lines  chan []byte
	buffer []byte
	eof    chan struct{}
}

func (t *turboManager) enable() {
	t.mode = true
	if t.lines == nil {
		t.lines = make(chan []byte, 1000) // Large buffer for performance
	}
	// Always create a new EOF channel for each batch of files.
	t.eof = make(chan struct{})
}

func (t *turboManager) enabled() bool {
	return t.mode
}

func (t *turboManager) hasEOF() bool {
	return t.eof != nil
}

func (t *turboManager) signalEOF() {
	if t.eof == nil {
		return
	}

	select {
	case <-t.eof:
		// Already closed
	default:
		close(t.eof)
	}
}

func (t *turboManager) channel() chan []byte {
	return t.lines
}

func (t *turboManager) channelLen() int {
	if t.lines == nil {
		return 0
	}
	return len(t.lines)
}

func (t *turboManager) flush(user *user.User) {
	if t.lines == nil {
		return
	}

	dlog.Server.Debug(user, "Flushing turbo data", "channelLen", len(t.lines))

	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-timeout:
			dlog.Server.Warn(user, "Timeout while flushing turbo data", "remaining", len(t.lines))
			return
		default:
			if len(t.lines) == 0 {
				dlog.Server.Debug(user, "Turbo channel drained successfully")
				return
			}
			// Give the reader time to process.
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// tryRead tries to serve data from turbo state and channels.
// Returns handled=false when caller should continue with normal path.
func (t *turboManager) tryRead(p []byte, user *user.User) (n int, handled bool) {
	if !t.mode {
		return 0, false
	}

	if len(t.buffer) > 0 {
		dlog.Server.Trace(user, "baseHandler.Read", "using buffered turbo data", "bufferedLen", len(t.buffer))
		n = copy(p, t.buffer)
		t.buffer = t.buffer[n:]
		dlog.Server.Trace(user, "baseHandler.Read", "after buffer read", "copied", n, "remaining", len(t.buffer))
		return n, true
	}

	if t.lines == nil {
		return 0, false
	}

	channelLen := len(t.lines)
	dlog.Server.Trace(user, "baseHandler.Read", "checking turboLines channel", "channelLen", channelLen)

	select {
	case turboData := <-t.lines:
		dlog.Server.Trace(user, "baseHandler.Read", "got data from turboLines", "dataLen", len(turboData))
		n = copy(p, turboData)
		if n < len(turboData) {
			t.buffer = turboData[n:]
			dlog.Server.Trace(user, "baseHandler.Read", "buffering remaining data", "bufferedLen", len(t.buffer))
		}
		return n, true
	default:
		if channelLen > 0 {
			dlog.Server.Trace(user, "baseHandler.Read", "channel has data but not available, waiting")
			time.Sleep(time.Millisecond)
			select {
			case turboData := <-t.lines:
				dlog.Server.Trace(user, "baseHandler.Read", "got data after wait", "dataLen", len(turboData))
				n = copy(p, turboData)
				if n < len(turboData) {
					t.buffer = turboData[n:]
				}
				return n, true
			default:
				// Still no data.
			}
		}

		if t.eof != nil {
			select {
			case <-t.eof:
				dlog.Server.Trace(user, "baseHandler.Read", "EOF received and channel empty, disabling turbo mode")
				t.mode = false
			default:
			}
		}

		dlog.Server.Trace(user, "baseHandler.Read", "no data in turboLines, falling through")
		return 0, false
	}
}
