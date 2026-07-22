package handlers

import (
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	user "github.com/mimecast/dtail/internal/user/server"
)

const (
	defaultOutputChannelBufferSize = 1000
	defaultOutputFlushTimeout      = 2 * time.Second
	defaultOutputFlushPollInterval = 10 * time.Millisecond
	defaultOutputReadRetryInterval = time.Millisecond
	defaultOutputEOFAckQuietPeriod = 50 * time.Millisecond
	defaultOutputEOFAckTimeout     = 2 * time.Second
)

type outputManagerConfig struct {
	channelBufferSize int
	flushTimeout      time.Duration
	flushPollInterval time.Duration
	readRetryInterval time.Duration
	eofAckQuietPeriod time.Duration
}

// outputManager coordinates output-mode state between command goroutines
// (enable/signalEOF/flush/waitForEOFAck, spawned per command by the server
// handler) and the session output goroutine (io.Copy -> baseHandler.Read ->
// tryRead). These run concurrently, so all mutable state is guarded by mu:
// mode, lines, buffer, eof, eofAck, eofEmptySince and epoch must only be
// accessed while holding mu. Channel *operations* (send/receive/close/len)
// are safe on a snapshot taken under mu; only the field reads/writes need
// the lock.
//
// The configuration fields (channelBufferSize, flushTimeout, ...) are
// deliberately not guarded: configure() runs exactly once from the handler
// constructor before any goroutine can touch the manager, so goroutine
// creation establishes the necessary happens-before edge.
type outputManager struct {
	mu     sync.Mutex
	mode   bool
	lines  chan []byte
	buffer []byte
	eof    chan struct{}
	eofAck chan struct{}

	// epoch is bumped by every enable() call and identifies the newest batch
	// that joined the output session. signalEOF only closes the EOF channel
	// when the signaler's captured epoch is still current, so a command that
	// decided "the batch is over" before another command joined cannot EOF
	// the newcomer's output (see signalEOF for the full protocol).
	epoch uint64

	channelBufferSize int
	flushTimeout      time.Duration
	flushPollInterval time.Duration
	readRetryInterval time.Duration
	eofAckQuietPeriod time.Duration

	eofEmptySince time.Time
}

// configure sets the tunables. It must be called before the manager is used
// concurrently (i.e. from the handler constructor); see the struct comment
// for why the config fields need no locking.
func (t *outputManager) configure(cfg outputManagerConfig) {
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

func (t *outputManager) resolvedChannelBufferSize() int {
	if t.channelBufferSize > 0 {
		return t.channelBufferSize
	}
	return defaultOutputChannelBufferSize
}

func (t *outputManager) resolvedFlushTimeout() time.Duration {
	if t.flushTimeout > 0 {
		return t.flushTimeout
	}
	return defaultOutputFlushTimeout
}

func (t *outputManager) resolvedFlushPollInterval() time.Duration {
	if t.flushPollInterval > 0 {
		return t.flushPollInterval
	}
	return defaultOutputFlushPollInterval
}

func (t *outputManager) resolvedReadRetryInterval() time.Duration {
	if t.readRetryInterval > 0 {
		return t.readRetryInterval
	}
	return defaultOutputReadRetryInterval
}

func (t *outputManager) resolvedEOFAckQuietPeriod() time.Duration {
	if t.eofAckQuietPeriod > 0 {
		return t.eofAckQuietPeriod
	}
	return defaultOutputEOFAckQuietPeriod
}

// enable atomically switches output mode on. It returns true when it
// transitioned from disabled to enabled and false when output mode was already
// active. Fresh EOF/EOF-ack channels are created on the off->on transition,
// so a concurrent (or repeated) enable can never yank a live EOF channel out
// from under an in-flight batch. The lines channel is created once and then
// reused for the lifetime of the session.
//
// One already-enabled case still refreshes the handshake channels: when the
// previous batch signaled EOF but the reader never acknowledged it (e.g.
// WaitForOutputEOFAck timed out on a slow client), t.eof is already closed.
// A new batch inheriting that closed channel would be disabled by
// maybeAckEOFLocked as soon as the lines channel is briefly empty, stranding
// the batch's remaining output. Refreshing is safe: the old eofAck is closed
// first, so a goroutine still blocked on it (e.g. the previous batch mid
// quiet-period) is released immediately instead of stalling until its
// timeout — its data was already flushed before it signaled EOF, and the
// reader can never acknowledge a replaced handshake anyway.
//
// Every enable() call — transition, join-while-enabled, or stale refresh —
// bumps the handshake epoch; see signalEOF.
func (t *outputManager) enable() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.epoch++

	if t.mode {
		if t.eofSignaledLocked() {
			// Stale handshake from an unacknowledged previous batch: release
			// its waiter and start a fresh handshake so the new batch is not
			// EOF'd prematurely.
			t.signalEOFAckLocked()
			t.resetEOFHandshakeLocked()
		}
		return false
	}
	t.mode = true
	if t.lines == nil {
		t.lines = make(chan []byte, t.resolvedChannelBufferSize())
	}
	// New batch of files: new EOF handshake channels.
	t.resetEOFHandshakeLocked()
	return true
}

// currentEpoch returns the handshake epoch to be captured by a command that
// is about to decide whether its batch is over. Capture it BEFORE checking
// the pending-work count: joiners increment the pending count before calling
// enable(), so a joiner that is invisible to a pending==0 check is guaranteed
// to bump the epoch after the capture, invalidating the stale signal.
func (t *outputManager) currentEpoch() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.epoch
}

// eofSignaledLocked reports whether the current EOF channel exists and has
// already been closed by signalEOF. The caller must hold t.mu.
func (t *outputManager) eofSignaledLocked() bool {
	if t.eof == nil {
		return false
	}
	select {
	case <-t.eof:
		return true
	default:
		return false
	}
}

// resetEOFHandshakeLocked mints fresh EOF handshake channels for a new batch
// and clears the quiet-period tracking. The caller must hold t.mu.
func (t *outputManager) resetEOFHandshakeLocked() {
	t.eof = make(chan struct{})
	t.eofAck = make(chan struct{})
	t.eofEmptySince = time.Time{}
}

func (t *outputManager) enabled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mode
}

func (t *outputManager) hasEOF() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.eof != nil
}

// signalEOF closes the EOF channel once, but only when the given epoch
// (captured via currentEpoch before the caller's pending-work check) is still
// current. If another command joined the output session in between — bumping
// the epoch via enable() — the signal is a stale "batch over" decision that
// predates the newcomer's output, and closing the (possibly shared) EOF
// channel would let the reader disable output mode mid-batch. Such stale
// signals are therefore dropped; the newcomer signals EOF itself when its
// batch drains. The stale signaler's WaitForOutputEOFAck then waits on the
// newcomer's handshake — released when that batch completes, or by timeout.
// Either way its own data was already flushed before it signaled.
//
// Joiners that never signal EOF themselves (output-aggregate/dmap commands,
// which are excluded from the cat/grep/tail EOF epilogue) also bump the
// epoch. A concurrently finishing cat's signal is then dropped and its ack
// wait deterministically times out. That degradation is bounded (one ack
// timeout) and loses no data: the cat's output was flushed before it
// signaled, and session shutdown flushes whatever remains.
func (t *outputManager) signalEOF(epoch uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if epoch != t.epoch {
		// A newer batch joined after the caller captured its epoch.
		return
	}

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

// signalEOFAckLocked closes the EOF-ack channel once. The caller must hold
// t.mu; callers are maybeAckEOFLocked (normal reader-side ack) and enable
// (releasing the previous batch's waiter when refreshing a stale handshake).
func (t *outputManager) signalEOFAckLocked() {
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

// waitForEOFAck blocks until the reader goroutine acknowledges the EOF or the
// timeout expires. The ack channel is snapshotted under the lock and waited on
// outside it, so a blocked waiter never stalls the reader that has to deliver
// the ack.
//
// A timeout <= 0 is clamped to the default (mirroring the fallback that
// OutputEOFAckTimeout uses) as defense-in-depth: every current handshake
// replacement leaves the old ack channel closed (a completed handshake was
// acked by the reader, and the stale refresh in enable() closes it
// explicitly), so a forever-wait cannot presently hang — but the clamp keeps
// that true for any future replacement path or caller.
func (t *outputManager) waitForEOFAck(timeout time.Duration) bool {
	t.mu.Lock()
	eofAck := t.eofAck
	t.mu.Unlock()

	if eofAck == nil {
		return true
	}

	if timeout <= 0 {
		timeout = defaultOutputEOFAckTimeout
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-eofAck:
		return true
	case <-timer.C:
		return false
	}
}

func (t *outputManager) channel() chan []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lines
}

func (t *outputManager) channelLen() int {
	t.mu.Lock()
	lines := t.lines
	t.mu.Unlock()

	if lines == nil {
		return 0
	}
	return len(lines)
}

// flush waits until the output lines channel drains or the flush timeout hits.
// It polls on a snapshot of the channel taken under the lock (the channel is
// never replaced once created), so the reader goroutine is never blocked by a
// flusher holding the lock across sleeps.
func (t *outputManager) flush(user *user.User) {
	t.mu.Lock()
	lines := t.lines
	t.mu.Unlock()

	if lines == nil {
		return
	}

	dlog.Server.Debug(user, "Flushing output data", "channelLen", len(lines))

	timeout := time.After(t.resolvedFlushTimeout())
	for {
		select {
		case <-timeout:
			dlog.Server.Warn(user, "Timeout while flushing output data", "remaining", len(lines))
			return
		default:
			if len(lines) == 0 {
				dlog.Server.Debug(user, "Output channel drained successfully")
				return
			}
			// Give the reader time to process.
			time.Sleep(t.resolvedFlushPollInterval())
		}
	}
}

// tryRead tries to serve data from output state and channels.
// Returns handled=false when caller should continue with normal path.
//
// It runs on the session output goroutine and holds t.mu for its entire
// duration except during the short retry sleep, so command goroutines calling
// enable/signalEOF/channelLen never observe torn state (e.g. mode=true with
// uninitialized channels).
//
// Lock ordering: the shouldDropGeneration callback is invoked (via
// consumeLocked) while t.mu is held and itself acquires sessionState.mu
// (sessionCommandState.currentGeneration), establishing the ordering
// output.mu -> sessionState.mu. Nothing may call into outputManager while
// holding sessionState.mu, or it would deadlock.
func (t *outputManager) tryRead(p []byte, user *user.User, shouldDropGeneration func(uint64) bool) (n int, handled bool) {
	// tryRead runs on the session output goroutine once per Read (i.e. per output
	// payload / ~64KB in server mode). Decide trace state once, before taking
	// the lock, so none of the per-read diagnostics below box their int/string
	// args or build a []interface{} when trace is off (the default). This also
	// shortens the t.mu hold time. Locking semantics are unchanged: the guard is
	// a pure branch and touches no lock. maxLevel is fixed at logger construction.
	traceEnabled := dlog.Server.TraceEnabled()

	t.mu.Lock()
	defer t.mu.Unlock()

	// Drain buffered remainder data first, regardless of mode: it belongs to a
	// payload that was already accepted for delivery. This is defensive — with
	// the current code the combination buffer-nonempty + mode-off cannot arise
	// (only maybeAckEOFLocked clears mode, and it runs only after the buffer
	// has been drained) — but draining first keeps delivery correct should
	// that invariant ever change.
	if len(t.buffer) > 0 {
		if traceEnabled {
			dlog.Server.Trace(user, "baseHandler.Read", "using buffered output data", "bufferedLen", len(t.buffer))
		}
		n = copy(p, t.buffer)
		t.buffer = t.buffer[n:]
		if traceEnabled {
			dlog.Server.Trace(user, "baseHandler.Read", "after buffer read", "copied", n, "remaining", len(t.buffer))
		}
		return n, true
	}

	if !t.mode {
		return 0, false
	}

	if t.lines == nil {
		return 0, false
	}

	if traceEnabled {
		dlog.Server.Trace(user, "baseHandler.Read", "checking outputLines channel", "channelLen", len(t.lines))
	}

	for {
		select {
		case outputData := <-t.lines:
			if n, delivered := t.consumeLocked(p, outputData, user, traceEnabled, shouldDropGeneration); delivered {
				return n, true
			}
			continue
		default:
		}

		// Recompute per iteration: after draining stale-generation entries the
		// channel may be empty, in which case the retry sleep is pointless.
		if len(t.lines) > 0 {
			if outputData, received := t.retryReceiveLocked(user, traceEnabled); received {
				if n, delivered := t.consumeLocked(p, outputData, user, traceEnabled, shouldDropGeneration); delivered {
					if traceEnabled {
						dlog.Server.Trace(user, "baseHandler.Read", "got data after wait")
					}
					return n, true
				}
				continue
			}
		}

		t.maybeAckEOFLocked(user)

		if traceEnabled {
			dlog.Server.Trace(user, "baseHandler.Read", "no data in outputLines, falling through")
		}
		return 0, false
	}
}

// retryReceiveLocked waits one retry interval for slow producers and then
// attempts a non-blocking receive from the lines channel. The lock is released
// during the sleep so command goroutines (enable, signalEOF, flush, ...) are
// not stalled by the reader's retry backoff; the lines channel is never
// replaced once created, so re-checking it after re-locking is safe. The
// caller must hold t.mu; it is held again on return.
func (t *outputManager) retryReceiveLocked(user *user.User, traceEnabled bool) (outputData []byte, received bool) {
	if traceEnabled {
		dlog.Server.Trace(user, "baseHandler.Read", "channel has data but not available, waiting")
	}

	retryInterval := t.resolvedReadRetryInterval()
	t.mu.Unlock()
	time.Sleep(retryInterval)
	t.mu.Lock()

	select {
	case outputData = <-t.lines:
		return outputData, true
	default:
		// Still no data.
		return nil, false
	}
}

// consumeLocked decodes a output payload, drops it when its generation is
// stale, and otherwise copies it into p, buffering any remainder for the next
// read. Returns delivered=false when the payload was dropped. The caller must
// hold t.mu.
func (t *outputManager) consumeLocked(p, outputData []byte, user *user.User,
	traceEnabled bool, shouldDropGeneration func(uint64) bool) (n int, delivered bool) {

	generation, decodedData := decodeGeneratedBytes(outputData)
	if shouldDropGeneration != nil && shouldDropGeneration(generation) {
		t.eofEmptySince = time.Time{}
		return 0, false
	}

	if traceEnabled {
		dlog.Server.Trace(user, "baseHandler.Read", "got data from outputLines", "dataLen", len(decodedData))
	}
	t.eofEmptySince = time.Time{}
	n = copy(p, decodedData)
	if n < len(decodedData) {
		t.buffer = decodedData[n:]
		if traceEnabled {
			dlog.Server.Trace(user, "baseHandler.Read", "buffering remaining data", "bufferedLen", len(t.buffer))
		}
	}
	return n, true
}

// maybeAckEOFLocked disables output mode and acknowledges the EOF once EOF has
// been signaled and the lines channel has stayed empty for the quiet period.
// The caller must hold t.mu.
func (t *outputManager) maybeAckEOFLocked(user *user.User) {
	if t.eof == nil {
		return
	}

	select {
	case <-t.eof:
	default:
		return
	}

	if len(t.lines) > 0 {
		t.eofEmptySince = time.Time{}
		return
	}

	if t.eofEmptySince.IsZero() {
		t.eofEmptySince = time.Now()
		return
	}

	if time.Since(t.eofEmptySince) >= t.resolvedEOFAckQuietPeriod() {
		dlog.Server.Trace(user, "baseHandler.Read", "EOF acknowledged and channel stable-empty, disabling output mode")
		t.mode = false
		t.signalEOFAckLocked()
	}
}
