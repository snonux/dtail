package loggers

import (
	"bufio"
	"bytes"
	"context"
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
	// sink is where the logger writes unless its output is redirected
	// (RedirectOutput).
	sink   io.Writer
	writer *bufio.Writer
	// redirect is the writer the output is redirected through, nil while
	// the logger writes to its own output.
	redirect   *wholeLines
	mutex      sync.Mutex
	resumeCond *sync.Cond
	pauseDepth int
	// hold, while set, is offered what is logged while the logger is paused
	// (HoldOutput): text it takes does not wait for the pause to end.
	hold func(w io.Writer, p []byte) bool
	// flushPending covers both an unread dirty notification and an armed
	// timer. Protected by mutex, it coalesces a burst into one notification.
	flushPending bool
	dirtyCh      chan struct{}
}

var _ Logger = (*stdout)(nil)
var _ Starter = (*stdout)(nil)
var _ Pauser = (*stdout)(nil)
var _ RawBytesWriter = (*stdout)(nil)
var _ OutputRedirector = (*stdout)(nil)
var _ OutputHolder = (*stdout)(nil)

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
	s := &stdout{sink: w, writer: bufio.NewWriterSize(w, stdoutWriterBufSize), dirtyCh: make(chan struct{}, 1)}
	s.resumeCond = sync.NewCond(&s.mutex)
	return s
}

// NewTerminal returns the terminal logger (the "stdout" logger: buffering,
// pause, hold, redirect) writing to w instead of os.Stdout, for the tests
// of the packages that pause and hold a client's terminal output.
func NewTerminal(w io.Writer) Logger {
	return newStdoutWriter(w)
}

func (s *stdout) Start(ctx context.Context, wg *sync.WaitGroup) {
	// Arm only on the first buffered write after a flush. Producers never
	// read the clock or reset a timer; an unused logger has no timer wakeups.
	go func() {
		defer wg.Done()
		start := time.Now()
		var timer *time.Timer
		var tick <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for {
			select {
			case <-s.dirtyCh:
				delay := nextFlushDelay(start, stdoutIdleFlushInterval)
				if timer == nil {
					timer = time.NewTimer(delay)
				} else {
					timer.Reset(delay)
				}
				tick = timer.C
			case <-tick:
				tick = nil
				s.flushIdle()
			case <-ctx.Done():
				// Another Start caller may still be alive. Release the
				// pending state along with this worker's final flush so
				// later writes can wake that worker (or a future Start).
				s.flushIdle()
				return
			}
		}
	}()
}

func (s *stdout) Log(message string) {
	s.log(message, true)
}

func (s *stdout) LogWithColors(message, coloredMessage string) {
	s.log(coloredMessage, true)
}

func (s *stdout) Raw(message string) {
	s.log(message, false)
}

func (s *stdout) RawWithColors(message, coloredMessage string) {
	s.log(coloredMessage, false)
}

// RawBytes writes payload bytes verbatim without a string conversion.
func (s *stdout) RawBytes(message []byte) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.waitUnpaused(func() []byte { return message }) {
		return
	}
	_, _ = s.writer.Write(message)
	s.armIdleFlush()
}

func (s *stdout) log(message string, nl bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.waitUnpaused(func() []byte { return heldText(message, nl) }) {
		return
	}

	// Buffered writes into the bufio.Writer batch many lines into one write
	// syscall. Errors are intentionally ignored: a logger that cannot write to
	// stdout has nowhere to report the failure.
	_, _ = s.writer.WriteString(message)
	if nl {
		_ = s.writer.WriteByte('\n')
	}
	s.armIdleFlush()
}

// armIdleFlush is called with mutex held. Flush/Pause leave a pending timer
// alone: any later write before it fires is covered by the same deadline.
func (s *stdout) armIdleFlush() {
	if !s.flushPending && s.writer.Buffered() > 0 {
		s.flushPending = true
		signal(s.dirtyCh)
	}
}

func (s *stdout) flushIdle() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	_ = s.writer.Flush()
	s.flushPending = false
}

// waitUnpaused is called with the mutex held and returns false once logging
// is not paused: the caller then writes. While the logger is paused and
// holds its output (HoldOutput), the text is offered to the hold instead;
// once the hold took it, waitUnpaused returns true and the caller is done.
// A hold that does not take it (it is full, or ended) leaves the caller
// waiting as without one.
func (s *stdout) waitUnpaused(text func() []byte) (held bool) {
	for s.pauseDepth > 0 {
		if s.hold != nil && s.hold(heldOutput{s: s}, text()) {
			return true
		}
		// Cond.Wait releases the mutex while logging is paused. This lets
		// Resume acquire it and prevents a waiting log call from blocking the
		// interactive path that owns the terminal.
		s.resumeCond.Wait()
	}
	return false
}

// heldText is what log writes for message: the message and, for nl, its
// line break, in one piece, so that a hold takes both or neither.
func heldText(message string, nl bool) []byte {
	text := make([]byte, 0, len(message)+1)
	text = append(text, message...)
	if nl {
		text = append(text, '\n')
	}
	return text
}

// HoldOutput makes the paused logger offer what is logged to take instead
// of blocking the caller: take reports whether it took the text p (copying
// it), which it writes to w, unchanged and in the order taken, before the
// pauser that held it resumes the logger. Text take refuses waits for the
// pause to end, as without a hold. A nil take ends the holding; text taken
// before stays with the hold. Writers already waiting are offered to the
// new hold at once.
func (s *stdout) HoldOutput(take func(w io.Writer, p []byte) bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.hold = take
	if take != nil {
		s.resumeCond.Broadcast()
	}
}

// heldOutput is the writer a hold gives the text it took back through
// (HoldOutput): it is written past the pause of the pauser that held it,
// as it was logged before the writers that still wait, and flushed, so
// that it stays in order with what the hold writes elsewhere next. That
// pauser calls it before its own Resume. While another pauser has the
// logger paused too (pause depth above one: the host key question, the
// Ctrl-C statistics), nothing is written and the error is one whose
// Paused method reports true: the caller keeps the text and tries again
// later, so held text never runs into what that pauser shows, all held
// text keeps one order, and the caller can still decide not to write.
type heldOutput struct{ s *stdout }

// heldOutputPaused is the error of a held text that can not be written
// now, as another pauser has the logger.
type heldOutputPaused struct{}

func (heldOutputPaused) Error() string { return "held output: the logger is paused by another pauser" }

// Paused marks the error for callers that know the logger only as a writer.
func (heldOutputPaused) Paused() bool { return true }

func (o heldOutput) Write(p []byte) (int, error) {
	s := o.s
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.pauseDepth > 1 {
		return 0, heldOutputPaused{}
	}
	_, err := s.writer.Write(p)
	_ = s.writer.Flush()
	return len(p), err
}

func (s *stdout) Pause() {
	// Flush before pausing so all output produced so far is visible before the
	// caller (interactive prompt / stats interrupt) writes directly to stdout,
	// preserving the ordering the unbuffered path used to give for free.
	s.mutex.Lock()
	defer s.mutex.Unlock()
	_ = s.writer.Flush()
	s.pauseDepth++
}

func (s *stdout) Resume() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.pauseDepth == 0 {
		return
	}

	s.pauseDepth--
	if s.pauseDepth == 0 {
		s.resumeCond.Broadcast()
	}
}

// RedirectOutput makes the logger write to w from now on, or to its own
// output again for a nil w. What is buffered is flushed to the output it
// was logged for first. w gets whole lines only (wholeLines): the buffer
// flushes itself when it is full, in the middle of a line, and w, the line
// editor of an interactive client, draws its prompt again below every
// write.
func (s *stdout) RedirectOutput(w io.Writer) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	_ = s.writer.Flush()
	if s.redirect != nil {
		s.redirect.flushTail()
		s.redirect = nil
	}
	if w == nil {
		s.writer.Reset(s.sink)
		return
	}
	s.redirect = &wholeLines{w: w}
	s.writer.Reset(s.redirect)
}

// wholeLines passes what is written to it on to w in whole lines: the end
// of a write behind its last line break is kept (tail) and written in
// front of the next write. A flush of the logger's buffer when it is full
// ends anywhere in a line; an idle flush ends with whatever was logged
// last, which for a log line is its line break. Text without a line break
// (payload written with Raw) is therefore held until its line ends or the
// redirect does (flushTail), except once it is longer than the logger's
// buffer: a line that long can not be kept whole.
type wholeLines struct {
	w    io.Writer
	tail []byte
}

func (l *wholeLines) Write(p []byte) (int, error) {
	end := bytes.LastIndexByte(p, '\n') + 1
	if end == 0 {
		if len(l.tail)+len(p) <= stdoutWriterBufSize {
			l.tail = append(l.tail, p...)
			return len(p), nil
		}
		end = len(p)
	}
	lines := p[:end]
	if len(l.tail) > 0 {
		lines = append(l.tail, lines...)
	}
	_, err := l.w.Write(lines)
	l.tail = append([]byte(nil), p[end:]...)
	return len(p), err
}

// flushTail writes the text kept behind the last line break.
func (l *wholeLines) flushTail() {
	if len(l.tail) > 0 {
		_, _ = l.w.Write(l.tail)
		l.tail = nil
	}
}

func (s *stdout) Flush() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	_ = s.writer.Flush()
}

func (*stdout) SupportsColors() bool { return true }
