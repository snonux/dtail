package clients

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mimecast/dtail/internal/clients/clientlog"
	"github.com/mimecast/dtail/internal/io/dlog/loggers"
)

// terminalTestLogger is a client logger over the real terminal logger of
// dlog (loggers.NewTerminal): its pause, hold and buffering are the ones a
// client run has.
type terminalTestLogger struct {
	clientlog.NopLogger
	out loggers.Logger
}

func (l *terminalTestLogger) Raw(message string) string {
	l.out.Raw(message)
	return message
}

func (l *terminalTestLogger) Pause()  { l.out.(loggers.Pauser).Pause() }
func (l *terminalTestLogger) Resume() { l.out.(loggers.Pauser).Resume() }

func (l *terminalTestLogger) HoldOutput(take func(io.Writer, []byte) bool) {
	l.out.(loggers.OutputHolder).HoldOutput(take)
}

// terminalHoldTest is a pause over the real terminal logger and the
// serverless gate, both writing to one terminal.
type terminalHoldTest struct {
	client     *baseClient
	pause      *interactivePause
	logger     *terminalTestLogger
	serverless io.Writer
	terminal   *orderedOutput
}

func newTerminalHoldTest() *terminalHoldTest {
	terminal := &orderedOutput{}
	logger := &terminalTestLogger{out: loggers.NewTerminal(terminal)}
	client := &baseClient{mu: newBaseClientMu(), loggers: LoggerDependencies{Client: logger},
		runtime: &clientRuntimeBoundary{outputGate: newOutputGate()}}
	return &terminalHoldTest{client: client, pause: client.newInteractivePause(), logger: logger,
		serverless: client.runtime.outputGate.writer(terminal), terminal: terminal}
}

// hold writes a log line and a serverless line with name during a wait.
func (h *terminalHoldTest) hold(name string) {
	h.pause.holdOutput(func() {
		h.logger.Raw(name + " log\n")
		_, _ = io.WriteString(h.serverless, name+" serverless\n")
	})
}

// printed returns what reached the terminal, the logger's buffer included.
func (h *terminalHoldTest) printed() string {
	h.logger.out.Flush()
	return h.terminal.String()
}

// TestHoldDrainKeepsOneOrderUnderAnotherPauser is the overlap of the Ctrl-C
// statistics with two Tab waits, on the real terminal logger: text held by
// the first wait is released while the statistics pause the logger, so it
// waits, the serverless text behind the log line too; the user types on
// at once, the statistics end (the text stays queued: the output is
// paused again), a second wait holds more text and the line is entered.
// All held text is printed in the order held: the second wait's never
// overtakes the first's, and a serverless line never the log line held
// before it.
func TestHoldDrainKeepsOneOrderUnderAnotherPauser(t *testing.T) {
	h := newTerminalHoldTest()
	h.pause.pauseTyping()
	h.hold("A")
	clientlog.Pause(h.logger) // Ctrl-C: the statistics pause the logger
	h.pause.resume()          // Enter within the statistics' pause
	h.pause.pauseTyping()     // typing on at once
	h.requireNothingPrinted(t, "while the statistics have the logger")
	clientlog.Resume(h.logger) // the statistics' pause ends
	h.requireNothingPrinted(t, "while the next line is typed")
	h.hold("B")
	waiting := startWrite(func() { h.logger.Raw("waited\n") })
	requireBlocked(t, waiting)
	h.pause.end()
	requireDone(t, waiting)
	want := "A log\nA serverless\nB log\nB serverless\nwaited\n"
	if got := h.printed(); got != want {
		t.Fatalf("terminal = %q, want %q", got, want)
	}
}

// requireNothingPrinted requires the terminal to stay empty for a while.
func (h *terminalHoldTest) requireNothingPrinted(t *testing.T, when string) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	if got := h.printed(); got != "" {
		t.Fatalf("terminal = %q %s, want the held text to stay queued", got, when)
	}
}

// TestHoldDrainWaitsWhilePausedAgain: held text released while the Ctrl-C
// statistics pause the logger is not written when their pause ends, if the
// output was paused again meanwhile, on the real terminal logger: by the
// first key of the next line, by an explicit pause (Enter twice), or with
// a Tab's wait running when the statistics end. It is written, complete,
// when that pause ends.
func TestHoldDrainWaitsWhilePausedAgain(t *testing.T) {
	for name, pausedAgain := range map[string]func(*testing.T, *terminalHoldTest){
		"typing": func(t *testing.T, h *terminalHoldTest) {
			h.pause.pauseTyping()
			clientlog.Resume(h.logger)
			h.requireNothingPrinted(t, "while the next line is typed")
		},
		"explicit pause": func(t *testing.T, h *terminalHoldTest) {
			h.pause.pause()
			clientlog.Resume(h.logger)
			h.requireNothingPrinted(t, "in the explicit pause")
		},
		"tab wait": func(t *testing.T, h *terminalHoldTest) {
			h.pause.pauseTyping()
			h.pause.holdOutput(func() {
				clientlog.Resume(h.logger)
				h.requireNothingPrinted(t, "during the Tab's wait")
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newTerminalHoldTest()
			h.pause.pauseTyping()
			h.hold("A")
			clientlog.Pause(h.logger) // the statistics
			h.pause.resume()          // Enter: A is queued, the statistics keep it
			pausedAgain(t, h)
			h.pause.end()
			if got, want := h.printed(), "A log\nA serverless\n"; got != want {
				t.Fatalf("terminal = %q, want %q", got, want)
			}
		})
	}
}

// stepWriter lets a write through for every step given; entered reports
// each write as it begins.
type stepWriter struct {
	steps   chan struct{}
	entered chan struct{}
	mu      sync.Mutex
	out     bytes.Buffer
}

func newStepWriter() *stepWriter {
	return &stepWriter{steps: make(chan struct{}), entered: make(chan struct{}, 64)}
}

func (s *stepWriter) Write(p []byte) (int, error) {
	s.entered <- struct{}{}
	<-s.steps
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.Write(p)
}

func (s *stepWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// TestHoldDrainStopsForANewPause: a pause that begins while the drain
// writes a large queue waits for the piece being written and stops the
// drain behind it, at a line's end: nothing more is written while paused,
// text a wait of that pause holds is queued behind the rest, and the
// resume writes all of it in order, before the write that waited.
func TestHoldDrainStopsForANewPause(t *testing.T) {
	out := newStepWriter()
	client := newPauseTestClient(newPausingTestLogger())
	pause := client.newInteractivePause()
	pause.hold.piece = 8 // two of the lines below
	serverless := client.runtime.outputGate.writer(out)

	pause.pauseTyping()
	pause.holdOutput(func() { _, _ = io.WriteString(serverless, "l0\nl1\nl2\nl3\nl4\nl5\n") })
	waiting := startWrite(func() { _, _ = io.WriteString(serverless, "waited\n") })
	pause.resume()
	<-out.entered // the drain writes the first piece
	paused := startWrite(func() { pause.pauseTyping() })
	requireBlocked(t, paused) // it waits for that piece
	out.steps <- struct{}{}
	requireDone(t, paused)
	close(out.steps) // every further write passes
	time.Sleep(100 * time.Millisecond)
	if got := out.String(); got != "l0\nl1\n" {
		t.Fatalf("output while paused again = %q, want only the piece that was being written", got)
	}
	pause.holdOutput(func() { _, _ = io.WriteString(serverless, "l6\n") })
	requireBlocked(t, waiting)

	pause.end()
	requireDone(t, waiting)
	if got, want := out.String(), "l0\nl1\nl2\nl3\nl4\nl5\nl6\nwaited\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestNextPiece: a piece ends at the last line break within the limit, a
// line longer than the limit is one piece, and text without a line break
// is written whole.
func TestNextPiece(t *testing.T) {
	for data, want := range map[string]string{
		"short\n":              "short\n",
		"ab\ncd\nef\n":         "ab\ncd\n",
		"a long line\nnext\n":  "a long line\n",
		"no line break at all": "no line break at all",
	} {
		if got := string(nextPiece([]byte(data), 8)); got != want {
			t.Errorf("nextPiece(%q, 8) = %q, want %q", data, got, want)
		}
	}
}

// TestHoldDrainEndIsBounded: the end of the control loop waits for held
// text that can not be written (output taking no write) only
// holdDrainWait; the drain goes on and writes it once the output does.
func TestHoldDrainEndIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stalled := &stalledWriter{open: make(chan struct{})}
		client := newPauseTestClient(newPausingTestLogger())
		pause := client.newInteractivePause()
		serverless := client.runtime.outputGate.writer(stalled)
		pause.pauseTyping()
		pause.holdOutput(func() { _, _ = io.WriteString(serverless, "held\n") })

		start := time.Now()
		pause.end()
		if waited := time.Since(start); waited != holdDrainWait {
			t.Fatalf("end waited %v, want %v", waited, holdDrainWait)
		}
		close(stalled.open)
		synctest.Wait()
		if got := stalled.String(); got != "held\n" {
			t.Fatalf("output = %q, want the held text written once the output took it", got)
		}
	})
}

// TestHoldDrainOrderStress runs six writers (three through the terminal
// logger, three through the serverless gate) against the control loop's
// pauses with waits that hold and a second pauser of the logger, under the
// race detector: every writer's lines must reach the terminal complete and
// in the order written.
func TestHoldDrainOrderStress(t *testing.T) {
	h := newTerminalHoldTest()
	const writers, lines = 6, 300
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range lines {
				line := fmt.Sprintf("w%d %d\n", w, i)
				if w%2 == 0 {
					h.logger.Raw(line)
				} else {
					_, _ = io.WriteString(h.serverless, line)
				}
				time.Sleep(50 * time.Microsecond)
			}
		})
	}
	stop := make(chan struct{})
	pauser := startWrite(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			clientlog.Pause(h.logger)
			time.Sleep(200 * time.Microsecond)
			clientlog.Resume(h.logger)
			time.Sleep(100 * time.Microsecond)
		}
	})
	written := make(chan struct{})
	go func() { wg.Wait(); close(written) }()
	for done := false; !done; {
		select {
		case <-written:
			done = true
		default:
		}
		h.pause.pauseTyping()
		h.pause.holdOutput(func() { time.Sleep(300 * time.Microsecond) })
		h.pause.resume()
	}
	close(stop)
	requireDone(t, pauser)
	h.pause.end()
	requireWritersInOrder(t, h.printed(), writers, lines)
}

// requireWritersInOrder requires output to hold, for each of the writers,
// its lines "wN I" for I from 0 to lines-1 in that order, and nothing else.
func requireWritersInOrder(t *testing.T, output string, writers, lines int) {
	t.Helper()
	next := make([]int, writers)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		writer, number, ok := strings.Cut(strings.TrimPrefix(line, "w"), " ")
		w, errW := strconv.Atoi(writer)
		n, errN := strconv.Atoi(number)
		if !ok || errW != nil || errN != nil || w >= writers || n != next[w] {
			t.Fatalf("line %q: order violation or broken line (writer %s expects %v)", line, writer, next)
		}
		next[w]++
	}
	for w, n := range next {
		if n != lines {
			t.Fatalf("writer %d: %d of %d lines printed", w, n, lines)
		}
	}
}

// stalledWriter takes no write until it is opened, like a pipe to a
// stopped reader.
type stalledWriter struct {
	open chan struct{}
	mu   sync.Mutex
	out  bytes.Buffer
}

func (s *stalledWriter) Write(p []byte) (int, error) {
	<-s.open
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.Write(p)
}

func (s *stalledWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// TestHoldDrainDoesNotBlockTheControlLoop: with output that takes no write
// (a pipe to a stopped reader), the resume returns without writing the
// held text itself, so the control loop goes on: it answers :show and
// takes :quit. The writers stay blocked behind the held text, and once the
// output takes writes again everything is written in order.
func TestHoldDrainDoesNotBlockTheControlLoop(t *testing.T) {
	stalled := &stalledWriter{open: make(chan struct{})}
	client := newPauseTestClient(newPausingTestLogger())
	pause := client.newInteractivePause()
	serverless := client.runtime.outputGate.writer(stalled)

	pause.pauseTyping()
	pause.holdOutput(func() { _, _ = io.WriteString(serverless, "held\n") })
	waiting := startWrite(func() { _, _ = io.WriteString(serverless, "waited\n") })
	requireBlocked(t, waiting)

	var tty bytes.Buffer
	lines := startWrite(func() {
		for _, line := range []string{":show", ":quit"} {
			if quit, err := client.handleInteractiveLine(&tty, line, pause); err != nil || quit != (line == ":quit") {
				t.Errorf("%s: quit=%v err=%v", line, quit, err)
			}
		}
	})
	requireDone(t, lines)
	if !strings.Contains(tty.String(), "workload=") {
		t.Fatalf(":show not answered while the output is stalled: %q", tty.String())
	}
	requireBlocked(t, waiting)

	close(stalled.open)
	pause.end()
	requireDone(t, waiting)
	if got := stalled.String(); got != "held\nwaited\n" {
		t.Fatalf("output = %q, want the held text before the write that waited", got)
	}
}
