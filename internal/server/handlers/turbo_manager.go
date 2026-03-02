package handlers

import (
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	user "github.com/mimecast/dtail/internal/user/server"
)

const (
	defaultTurboChannelBufferSize = 1000
	defaultTurboFlushTimeout      = 2 * time.Second
	defaultTurboFlushPollInterval = 10 * time.Millisecond
	defaultTurboReadRetryInterval = time.Millisecond
	defaultTurboEOFAckQuietPeriod = 50 * time.Millisecond
)

type turboManagerConfig struct {
	channelBufferSize int
	flushTimeout      time.Duration
	flushPollInterval time.Duration
	readRetryInterval time.Duration
	eofAckQuietPeriod time.Duration
}

type turboManager struct {
	mode   bool
	lines  chan []byte
	buffer []byte
	eof    chan struct{}
	eofAck chan struct{}

	channelBufferSize int
	flushTimeout      time.Duration
	flushPollInterval time.Duration
	readRetryInterval time.Duration
	eofAckQuietPeriod time.Duration

	eofEmptySince time.Time
}

func (t *turboManager) configure(cfg turboManagerConfig) {
	if cfg.channelBufferSize > 0 {
		t.channelBufferSize = cfg.channelBufferSize
	}
	if cfg.flushTimeout > 0 {
		t.flushTimeout = cfg.flushTimeout
	}
	if cfg.flushPollInterval > 0 {
		t.flushPollInterval = cfg.flushPollInterval
	}
	if cfg.readRetryInterval > 0 {
		t.readRetryInterval = cfg.readRetryInterval
	}
	if cfg.eofAckQuietPeriod > 0 {
		t.eofAckQuietPeriod = cfg.eofAckQuietPeriod
	}
}

func (t *turboManager) resolvedChannelBufferSize() int {
	if t.channelBufferSize > 0 {
		return t.channelBufferSize
	}
	return defaultTurboChannelBufferSize
}

func (t *turboManager) resolvedFlushTimeout() time.Duration {
	if t.flushTimeout > 0 {
		return t.flushTimeout
	}
	return defaultTurboFlushTimeout
}

func (t *turboManager) resolvedFlushPollInterval() time.Duration {
	if t.flushPollInterval > 0 {
		return t.flushPollInterval
	}
	return defaultTurboFlushPollInterval
}

func (t *turboManager) resolvedReadRetryInterval() time.Duration {
	if t.readRetryInterval > 0 {
		return t.readRetryInterval
	}
	return defaultTurboReadRetryInterval
}

func (t *turboManager) resolvedEOFAckQuietPeriod() time.Duration {
	if t.eofAckQuietPeriod > 0 {
		return t.eofAckQuietPeriod
	}
	return defaultTurboEOFAckQuietPeriod
}

func (t *turboManager) enable() {
	t.mode = true
	if t.lines == nil {
		t.lines = make(chan []byte, t.resolvedChannelBufferSize())
	}
	// Always create a new EOF channel for each batch of files.
	t.eof = make(chan struct{})
	t.eofAck = make(chan struct{})
	t.eofEmptySince = time.Time{}
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

func (t *turboManager) signalEOFAck() {
	if t.eofAck == nil {
		return
	}

	select {
	case <-t.eofAck:
		// Already closed.
	default:
		close(t.eofAck)
	}
}

func (t *turboManager) waitForEOFAck(timeout time.Duration) bool {
	if t.eofAck == nil {
		return true
	}

	if timeout <= 0 {
		<-t.eofAck
		return true
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-t.eofAck:
		return true
	case <-timer.C:
		return false
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

	timeout := time.After(t.resolvedFlushTimeout())
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
			time.Sleep(t.resolvedFlushPollInterval())
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
		t.eofEmptySince = time.Time{}
		n = copy(p, turboData)
		if n < len(turboData) {
			t.buffer = turboData[n:]
			dlog.Server.Trace(user, "baseHandler.Read", "buffering remaining data", "bufferedLen", len(t.buffer))
		}
		return n, true
	default:
		if channelLen > 0 {
			dlog.Server.Trace(user, "baseHandler.Read", "channel has data but not available, waiting")
			time.Sleep(t.resolvedReadRetryInterval())
			select {
			case turboData := <-t.lines:
				dlog.Server.Trace(user, "baseHandler.Read", "got data after wait", "dataLen", len(turboData))
				t.eofEmptySince = time.Time{}
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
				if len(t.lines) > 0 {
					t.eofEmptySince = time.Time{}
					break
				}

				if t.eofEmptySince.IsZero() {
					t.eofEmptySince = time.Now()
					break
				}

				if time.Since(t.eofEmptySince) >= t.resolvedEOFAckQuietPeriod() {
					dlog.Server.Trace(user, "baseHandler.Read", "EOF acknowledged and channel stable-empty, disabling turbo mode")
					t.mode = false
					t.signalEOFAck()
				}
			default:
			}
		}

		dlog.Server.Trace(user, "baseHandler.Read", "no data in turboLines, falling through")
		return 0, false
	}
}
