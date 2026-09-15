package server

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// testWaitLimit only guards against hangs; no assertion depends on it.
const testWaitLimit = 5 * time.Second

type fakeTicker struct {
	ch       chan time.Time
	stopped  chan struct{}
	stopOnce sync.Once
}

func newFakeTicker() *fakeTicker {
	return &fakeTicker{ch: make(chan time.Time), stopped: make(chan struct{})}
}

func (t *fakeTicker) Chan() <-chan time.Time { return t.ch }

func (t *fakeTicker) Stop() { t.stopOnce.Do(func() { close(t.stopped) }) }

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// deadlineRecorder records deadlines instead of applying them, so fake clock
// values never expire real pipe I/O.
type deadlineRecorder struct {
	net.Conn

	mu        sync.Mutex
	deadlines []time.Time
	err       error
}

func (c *deadlineRecorder) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.deadlines = append(c.deadlines, t)
	return nil
}

func (c *deadlineRecorder) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

func (c *deadlineRecorder) snapshot() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.deadlines...)
}

type activityHarness struct {
	conn        *activityConn
	peer        net.Conn
	rec         *deadlineRecorder
	ticker      *fakeTicker
	clock       *fakeClock
	tickerCalls int
	interval    time.Duration
}

func newActivityHarness(t *testing.T) *activityHarness {
	t.Helper()
	local, peer := net.Pipe()
	h := &activityHarness{
		peer:   peer,
		rec:    &deadlineRecorder{Conn: local},
		ticker: newFakeTicker(),
		clock:  &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
	}
	h.conn = newActivityConn(h.rec)
	h.conn.now = h.clock.Now
	h.conn.newTicker = func(interval time.Duration) activityTicker {
		h.tickerCalls++
		h.interval = interval
		return h.ticker
	}
	t.Cleanup(func() {
		_ = h.conn.Close()
		_ = peer.Close()
	})
	return h
}

func (h *activityHarness) enable(t *testing.T, ctx context.Context, timeout time.Duration) {
	t.Helper()
	if err := h.conn.enable(ctx, timeout); err != nil {
		t.Fatalf("enable(%v): %v", timeout, err)
	}
}

// sendTick blocks until the refresher receives one tick.
func (h *activityHarness) sendTick(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(testWaitLimit)
	defer timer.Stop()
	select {
	case h.ticker.ch <- h.clock.Now():
	case <-timer.C:
		t.Fatal("refresher did not receive tick")
	}
}

// tick delivers one tick and returns only after the refresher handled it: the
// unbuffered barrier tick can be received only once the first one is done.
// The barrier sees no activity because the test performs none in between.
func (h *activityHarness) tick(t *testing.T) {
	t.Helper()
	h.sendTick(t)
	h.sendTick(t)
}

func (h *activityHarness) waitStopped(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(testWaitLimit)
	defer timer.Stop()
	select {
	case <-h.ticker.stopped:
	case <-timer.C:
		t.Fatal("refresher did not stop")
	}
}

func (h *activityHarness) write(t *testing.T) {
	t.Helper()
	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(h.peer, make([]byte, 1))
		readErr <- err
	}()
	if _, err := h.conn.Write([]byte{'x'}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := <-readErr; err != nil {
		t.Fatalf("peer read: %v", err)
	}
}

func (h *activityHarness) read(t *testing.T) {
	t.Helper()
	writeErr := make(chan error, 1)
	go func() {
		_, err := h.peer.Write([]byte{'x'})
		writeErr <- err
	}()
	if _, err := io.ReadFull(h.conn, make([]byte, 1)); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("peer write: %v", err)
	}
}

func (h *activityHarness) wantDeadlines(t *testing.T, want ...time.Time) {
	t.Helper()
	got := h.rec.snapshot()
	if len(got) != len(want) {
		t.Fatalf("deadlines = %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("deadline[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestIdleRefreshInterval(t *testing.T) {
	tests := []struct {
		timeout time.Duration
		want    time.Duration
	}{
		{timeout: time.Millisecond, want: time.Second},
		{timeout: time.Second, want: time.Second},
		{timeout: 4 * time.Second, want: time.Second},
		{timeout: 8 * time.Second, want: 2 * time.Second},
		{timeout: time.Minute, want: 15 * time.Second},
		{timeout: 2 * time.Minute, want: 30 * time.Second},
		{timeout: 15 * time.Minute, want: 30 * time.Second},
	}
	for _, tt := range tests {
		if got := idleRefreshInterval(tt.timeout); got != tt.want {
			t.Errorf("idleRefreshInterval(%v) = %v, want %v", tt.timeout, got, tt.want)
		}
	}
}

func TestActivityConnEnableSetsInitialDeadline(t *testing.T) {
	h := newActivityHarness(t)
	const timeout = time.Minute
	h.enable(t, t.Context(), timeout)

	if h.tickerCalls != 1 || h.interval != 15*time.Second {
		t.Fatalf("ticker calls = %d interval = %v, want 1 call with 15s", h.tickerCalls, h.interval)
	}
	h.wantDeadlines(t, h.clock.Now().Add(timeout+h.interval))
}

func TestActivityConnActivityExtendsDeadlineOnTick(t *testing.T) {
	tests := []struct {
		name     string
		activity func(*activityHarness, *testing.T)
	}{
		{name: "read", activity: (*activityHarness).read},
		{name: "write", activity: (*activityHarness).write},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newActivityHarness(t)
			const timeout = time.Minute
			h.enable(t, t.Context(), timeout)
			initial := h.clock.Now().Add(timeout + h.interval)

			tt.activity(h, t)
			// I/O itself must not touch the deadline.
			h.wantDeadlines(t, initial)

			h.clock.advance(h.interval)
			h.tick(t)
			h.wantDeadlines(t, initial, h.clock.Now().Add(timeout+h.interval))
		})
	}
}

func TestActivityConnIdleTicksDoNotExtendDeadline(t *testing.T) {
	h := newActivityHarness(t)
	const timeout = time.Minute
	h.enable(t, t.Context(), timeout)
	initial := h.clock.Now().Add(timeout + h.interval)

	for range 5 {
		h.clock.advance(h.interval)
		h.tick(t)
	}
	h.wantDeadlines(t, initial)
}

func TestActivityConnActivityBeforeEnableIsNotCarriedOver(t *testing.T) {
	h := newActivityHarness(t)
	h.write(t) // handshake traffic
	h.enable(t, t.Context(), time.Minute)
	initial := h.rec.snapshot()

	h.tick(t)
	h.wantDeadlines(t, initial...)
}

func TestActivityConnActivityIsConsumedOncePerTick(t *testing.T) {
	h := newActivityHarness(t)
	const timeout = time.Minute
	h.enable(t, t.Context(), timeout)
	initial := h.clock.Now().Add(timeout + h.interval)

	// A burst of activity just before a tick yields exactly one refresh.
	for range 10 {
		h.write(t)
	}
	h.clock.advance(time.Second)
	h.tick(t)
	refreshed := h.clock.Now().Add(timeout + h.interval)
	h.wantDeadlines(t, initial, refreshed)

	// The flag was cleared: the following idle tick must not extend again.
	h.clock.advance(h.interval)
	h.tick(t)
	h.wantDeadlines(t, initial, refreshed)

	// New activity after the tick is picked up by the next one.
	h.read(t)
	h.clock.advance(h.interval)
	h.tick(t)
	h.wantDeadlines(t, initial, refreshed, h.clock.Now().Add(timeout+h.interval))
}

func TestActivityConnDeadlineOutlastsIdleGapUpToTimeout(t *testing.T) {
	h := newActivityHarness(t)
	const timeout = time.Minute
	h.enable(t, t.Context(), timeout)
	deadline := h.clock.Now().Add(timeout + h.interval)

	// Stay idle for the full timeout, ticking on schedule, then become active
	// right after a tick: the worst case for picking up the activity.
	for elapsed := time.Duration(0); elapsed < timeout; elapsed += h.interval {
		h.clock.advance(h.interval)
		h.tick(t)
	}
	h.write(t)
	h.clock.advance(h.interval)
	if h.clock.Now().After(deadline) {
		t.Fatalf("next tick at %v is after deadline %v; active session would close", h.clock.Now(), deadline)
	}
	h.tick(t)
	if got := h.rec.snapshot(); len(got) != 2 {
		t.Fatalf("deadlines = %v, want refresh after late activity", got)
	}
}

func TestActivityConnCloseStopsRefresher(t *testing.T) {
	h := newActivityHarness(t)
	h.enable(t, t.Context(), time.Minute)
	h.write(t)

	if err := h.conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-h.ticker.stopped:
	default:
		t.Fatal("Close returned before the refresher stopped")
	}
	if len(h.rec.snapshot()) != 1 {
		t.Fatalf("deadlines = %v, want no refresh after Close", h.rec.snapshot())
	}
	if err := h.conn.enable(t.Context(), time.Minute); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("enable after Close = %v, want net.ErrClosed", err)
	}
}

func TestActivityConnContextCancelStopsRefresher(t *testing.T) {
	h := newActivityHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	h.enable(t, ctx, time.Minute)

	h.write(t)
	cancel()
	h.waitStopped(t)
	if len(h.rec.snapshot()) != 1 {
		t.Fatalf("deadlines = %v, want no refresh after cancel", h.rec.snapshot())
	}
}

func TestActivityConnSetDeadlineErrorStopsRefresher(t *testing.T) {
	h := newActivityHarness(t)
	h.enable(t, t.Context(), time.Minute)

	h.rec.setErr(net.ErrClosed)
	h.write(t)
	h.sendTick(t)
	h.waitStopped(t)
}

func TestActivityConnDisabledTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		h := newActivityHarness(t)
		h.enable(t, t.Context(), timeout)

		if h.tickerCalls != 0 {
			t.Fatalf("timeout %v started a refresher", timeout)
		}
		h.wantDeadlines(t, time.Time{})
		h.write(t)
		if err := h.conn.Close(); err != nil {
			t.Fatalf("close with timeout %v: %v", timeout, err)
		}
	}
}

func TestActivityConnEnableTwiceFails(t *testing.T) {
	h := newActivityHarness(t)
	h.enable(t, t.Context(), time.Minute)
	if err := h.conn.enable(t.Context(), time.Minute); !errors.Is(err, errIdleDeadlineEnabled) {
		t.Fatalf("second enable = %v, want errIdleDeadlineEnabled", err)
	}
	if h.tickerCalls != 1 {
		t.Fatalf("ticker calls = %d, want 1", h.tickerCalls)
	}
}

func TestActivityConnConcurrentActivityTicksAndClose(t *testing.T) {
	h := newActivityHarness(t)
	h.enable(t, t.Context(), time.Minute)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(io.Discard, h.peer)
	}()
	go func() {
		defer wg.Done()
		for {
			if _, err := h.conn.Write([]byte("payload")); err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case h.ticker.ch <- time.Time{}:
			case <-h.ticker.stopped:
				return
			}
		}
	}()

	for len(h.rec.snapshot()) < 3 {
		h.clock.advance(time.Second)
	}
	if err := h.conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	afterClose := len(h.rec.snapshot())
	wg.Wait()
	if got := len(h.rec.snapshot()); got != afterClose {
		t.Fatalf("deadlines grew from %d to %d after Close returned", afterClose, got)
	}
}

func TestActivityConnTimesOutWhenIdle(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()

	conn := newActivityConn(serverConn)
	defer func() { _ = conn.Close() }()
	const timeout = 50 * time.Millisecond
	if err := conn.enable(t.Context(), timeout); err != nil {
		t.Fatalf("enable idle timeout: %v", err)
	}

	started := time.Now()
	_, err := conn.Read(make([]byte, 1))
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("idle read error = %v, want network timeout", err)
	}
	if elapsed := time.Since(started); elapsed < timeout {
		t.Fatalf("idle read timed out too early after %v", elapsed)
	}
}
