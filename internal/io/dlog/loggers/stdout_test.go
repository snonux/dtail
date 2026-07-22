package loggers

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingWriter records how many times Write is called and accumulates all
// bytes, so tests can assert that buffering batches many logical lines into a
// small number of underlying writes while preserving content and order.
type countingWriter struct {
	mutex  sync.Mutex
	buf    bytes.Buffer
	writes int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.writes++
	return c.buf.Write(p)
}

func (c *countingWriter) String() string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.buf.String()
}

func (c *countingWriter) Writes() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.writes
}

// TestStdoutBuffersAndPreservesOrder proves the buffered stdout path batches
// many lines into far fewer underlying writes than one-per-line, and that a
// Flush emits the exact content in order (nothing dropped or reordered).
func TestStdoutBuffersAndPreservesOrder(t *testing.T) {
	cw := &countingWriter{}
	s := newStdoutWriter(cw)

	const n = 1000
	var want strings.Builder
	for i := 0; i < n; i++ {
		line := "line-" + strconv.Itoa(i)
		s.Raw(time.Now(), line+"\n")
		want.WriteString(line + "\n")
	}

	// Before flush the small lines must still be batched in the bufio buffer:
	// with per-line writes this would already be n writes.
	if got := cw.Writes(); got >= n {
		t.Fatalf("expected buffering to batch writes, got %d writes for %d lines", got, n)
	}

	s.Flush()

	if got := cw.String(); got != want.String() {
		t.Fatalf("content mismatch after flush:\n got %q\nwant %q", got, want.String())
	}
	// 1000 short lines fit in a handful of 64KB flushes, definitely far below n.
	if got := cw.Writes(); got > n/10 {
		t.Fatalf("expected far fewer than %d writes, got %d", n/10, got)
	}
}

// TestStdoutFlushOnPause proves output produced before Pause() is flushed to
// the sink before Pause returns, so an interactive prompt writing directly to
// the terminal never appears ahead of already-logged output.
func TestStdoutFlushOnPause(t *testing.T) {
	cw := &countingWriter{}
	s := newStdoutWriter(cw)

	s.Raw(time.Now(), "before-pause\n")

	// Pause blocks on the pauseCh handshake until a concurrent log() consumes
	// the token (the existing pause semantics). Drive a stream of log() calls
	// so one is guaranteed to pick up the token and let Pause() return,
	// mirroring the real prompt flow where logging goroutines are active.
	paused := make(chan struct{})
	go func() {
		s.Pause()
		close(paused)
	}()
	go func() {
		for {
			select {
			case <-paused:
				return
			default:
				s.Log(time.Now(), "consumes-pause")
				time.Sleep(time.Millisecond)
			}
		}
	}()
	<-paused

	// Pause() flushes before the handshake, so the pre-pause line must already
	// be in the sink now regardless of the buffer.
	if got := cw.String(); !strings.Contains(got, "before-pause") {
		t.Fatalf("expected buffered output flushed on Pause, got %q", got)
	}
	s.Resume()
}

// TestStdoutIdleFlush proves that a single low-volume line (follow/tail style)
// is not stuck behind the buffer: the Start() idle ticker flushes it promptly
// without any explicit Flush call.
func TestStdoutIdleFlush(t *testing.T) {
	cw := &countingWriter{}
	s := newStdoutWriter(cw)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	s.Start(ctx, &wg)

	s.Raw(time.Now(), "follow-line\n")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(cw.String(), "follow-line") {
			cancel()
			wg.Wait()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	t.Fatal("follow-style line stuck behind buffer; idle flush did not emit it")
}

// TestStdoutFinalFlushOnClose proves no buffered output is lost on clean
// shutdown: data logged just before ctx cancel is flushed before the Start
// goroutine (and thus wg.Wait) returns.
func TestStdoutFinalFlushOnClose(t *testing.T) {
	cw := &countingWriter{}
	s := newStdoutWriter(cw)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	s.Start(ctx, &wg)

	s.Raw(time.Now(), "last-line-before-exit\n")
	cancel()
	wg.Wait()

	if got := cw.String(); !strings.Contains(got, "last-line-before-exit") {
		t.Fatalf("buffered output lost on shutdown, got %q", got)
	}
}

// Regression: during an interactive prompt, dlog.Common.Pause() unblocks when some
// goroutine hits stdout.log(); that goroutine must not hold the stdout mutex while
// waiting on resume, or dlog.Client.Info from the prompt callback deadlocks forever.
func TestStdoutSecondLogDuringPauseWaitDoesNotDeadlock(t *testing.T) {
	s := newStdout()

	go s.Pause()
	time.Sleep(50 * time.Millisecond)

	go func() {
		s.Log(time.Now(), "first log consumes pause and waits on resume")
	}()
	time.Sleep(50 * time.Millisecond)

	secondDone := make(chan struct{})
	go func() {
		s.Log(time.Now(), "second log must acquire mutex while first waits for Resume")
		close(secondDone)
	}()

	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: second Log blocked on mutex while first waits for Resume")
	}

	s.Resume()
	time.Sleep(50 * time.Millisecond)
}
