package loggers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	// stdoutWriterBufSize is the size of the bufio buffer wrapping os.Stdout.
	// The old path did one fmt.Println (one write syscall) per received line;
	// buffering lets bulk payload batch into ~one syscall per bufferful. bufio
	// auto-flushes when full so high-throughput output never stalls.
	stdoutWriterBufSize = 64 * 1024
	// stdoutIdleFlushInterval bounds how long buffered output may sit unwritten
	// when output goes idle (follow/interactive trickling a few lines). Without
	// it, low-volume output would be stuck behind the buffer, so follow/tail
	// would appear frozen on the terminal.
	stdoutIdleFlushInterval = 100 * time.Millisecond
)

type stdout struct {
	pauseCh  chan struct{}
	resumeCh chan struct{}
	writer   *bufio.Writer
	mutex    sync.Mutex
}

var _ Logger = (*stdout)(nil)

func newStdout() *stdout {
	return newStdoutWriter(os.Stdout)
}

// newStdoutWriter builds a stdout logger over an arbitrary sink. Production
// uses os.Stdout; tests inject a counting writer to assert that buffering
// batches many lines into few underlying writes. The bufio writer is created
// eagerly so the logger is usable even when Start() is never called (e.g. in
// isolated unit tests); idle/shutdown flushing is only driven once Start()
// spawns the flush goroutine.
func newStdoutWriter(w io.Writer) *stdout {
	return &stdout{
		pauseCh:  make(chan struct{}),
		resumeCh: make(chan struct{}),
		writer:   bufio.NewWriterSize(w, stdoutWriterBufSize),
	}
}

func (s *stdout) Start(ctx context.Context, wg *sync.WaitGroup) {
	// Background flusher: with a real buffer, low-volume (follow/interactive)
	// output would otherwise sit unwritten until the buffer fills. The ticker
	// flushes any partial buffer promptly, and ctx.Done triggers a final flush
	// so no buffered output is lost on clean shutdown. wg.Done is deferred to
	// the goroutine so callers (ClientRuntime.Stop -> wg.Wait) block until the
	// final flush has happened.
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(stdoutIdleFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.Flush()
			case <-ctx.Done():
				s.Flush()
				return
			}
		}
	}()
}

func (s *stdout) Log(now time.Time, message string) {
	s.log(message, true)
}

func (s *stdout) LogWithColors(now time.Time, message, coloredMessage string) {
	s.log(coloredMessage, true)
}

func (s *stdout) Raw(now time.Time, message string) {
	s.log(message, false)
}

func (s *stdout) RawWithColors(now time.Time, message, coloredMessage string) {
	s.log(coloredMessage, false)
}

func (s *stdout) log(message string, nl bool) {
	s.mutex.Lock()
	select {
	case <-s.pauseCh:
		// Wait for Resume without holding the mutex: the prompt path calls
		// dlog after the user answers while Pause is still active; holding the
		// mutex here would deadlock (Info blocks on Lock, Resume never runs).
		s.mutex.Unlock()
		<-s.resumeCh
		s.mutex.Lock()
	default:
	}
	defer s.mutex.Unlock()

	// Buffered writes: fmt.Fprint(ln) into the bufio.Writer batches many lines
	// into one write syscall. Errors are intentionally ignored — a logger that
	// cannot write to stdout has nowhere to report the failure.
	if nl {
		_, _ = fmt.Fprintln(s.writer, message)
		return
	}
	_, _ = fmt.Fprint(s.writer, message)
}

func (s *stdout) Pause() {
	// Flush before pausing so all output produced so far is visible before the
	// caller (interactive prompt / stats interrupt) writes directly to stdout,
	// preserving the ordering the unbuffered path used to give for free. The
	// pauseCh handshake below is unchanged so the pause semantics (and the
	// deadlock guarantees exercised by the unit tests) are preserved.
	s.mutex.Lock()
	_ = s.writer.Flush()
	s.mutex.Unlock()
	s.pauseCh <- struct{}{}
}

func (s *stdout) Resume() { s.resumeCh <- struct{}{} }

func (s *stdout) Flush() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	// bufio.Flush is a no-op when nothing is buffered, so calling this on every
	// idle tick is cheap.
	_ = s.writer.Flush()
}

func (s *stdout) Rotate() {
	// This is empty because it isn't doing anything but has to satisfy the interface.
}

func (*stdout) SupportsColors() bool { return true }
