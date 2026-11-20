package clients

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
)

// orderedOutput records what several writers write, in one order, as a
// terminal shows the client logger's and the serverless direct output.
type orderedOutput struct {
	mu  sync.Mutex
	out strings.Builder
}

func (o *orderedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.out.Write(p)
}

func (o *orderedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.out.String()
}

// TestOutputHold: the hold takes text only while started, keeps the texts
// of several writers in the order taken, takes nothing more once a text
// did not fit (so that no later text overtakes it), and release queues
// everything for writeNext, which writes it once, in order, also over
// several releases; held and queued text count towards the bound.
func TestOutputHold(t *testing.T) {
	a, b := &bytes.Buffer{}, &bytes.Buffer{}
	var order orderedOutput
	toA, toB := io.MultiWriter(a, &order), io.MultiWriter(b, &order)
	hold := newOutputHold(16)

	if hold.take(toA, []byte("early")) {
		t.Fatal("a hold not started took text")
	}
	hold.start()
	text := []byte("a1 ")
	for _, held := range []bool{hold.take(toA, text), hold.take(toA, []byte("a2 ")),
		hold.take(toB, []byte("b1 ")), hold.take(toA, []byte("a3 "))} {
		if !held {
			t.Fatal("the started hold refused text that fits")
		}
	}
	copy(text, "XX ") // the hold copied it
	if len(hold.held) != 3 {
		t.Fatalf("%d chunks, want consecutive texts of one writer kept as one (3)", len(hold.held))
	}
	if hold.take(toB, []byte("too long")) || hold.take(toB, []byte("b")) {
		t.Fatal("the hold took text beyond its bound, or a small one after it was full")
	}
	hold.stop()
	if hold.take(toA, []byte("a")) || order.String() != "" {
		t.Fatalf("stopped hold took text, or wrote before its release: %q", order.String())
	}

	if !hold.isFull() || !hold.release() || hold.isFull() || order.String() != "" {
		t.Fatalf("release: full %v, written %q; want the text queued, not yet written", hold.isFull(), order.String())
	}
	// Text held after the release is queued behind it by the next one.
	hold.start()
	if !hold.take(toB, []byte("b2 ")) || hold.take(toB, []byte("b3 ")) {
		t.Fatal("the bound does not count the queued text, or the released hold takes nothing")
	}
	hold.release()
	for hold.writeNext() == heldWrote {
	}
	if order.String() != "a1 a2 b1 a3 b2 " || a.String() != "a1 a2 a3 " || b.String() != "b1 b2 " {
		t.Fatalf("written %q (a %q, b %q)", order.String(), a.String(), b.String())
	}
	if hold.release() || hold.hasQueued() || hold.size != 0 || !hold.take(toB, []byte("0123456789abcdef")) {
		t.Fatalf("the written hold is not empty: size %d", hold.size)
	}
}

// TestNilOutputHold: a nil hold takes, queues and writes nothing.
func TestNilOutputHold(t *testing.T) {
	var none *outputHold
	none.start()
	none.stop()
	if none.take(io.Discard, []byte("x")) || none.release() || none.writeNext() != heldEmpty || none.hasQueued() || none.isFull() {
		t.Fatal("nil hold took, queued or wrote text")
	}
}

// newHoldTest returns a paused (for typing) pause whose logger and
// serverless gate write to one ordered output.
func newHoldTest(max int) (*interactivePause, *pausingTestLogger, io.Writer, *orderedOutput) {
	terminal := &orderedOutput{}
	logger := newPausingTestLogger()
	logger.out = terminal
	client := newPauseTestClient(logger)
	pause := client.newInteractivePause()
	pause.hold = newOutputHold(max)
	pause.pauseTyping()
	return pause, logger, client.runtime.outputGate.writer(terminal), terminal
}

// TestHoldOutputKeepsOneOrder: during holdOutput the client logger and the
// serverless direct output go on without anything reaching the terminal;
// afterwards they block again, and the resume prints the held text first,
// in the order written across both, then what waited.
func TestHoldOutputKeepsOneOrder(t *testing.T) {
	pause, logger, serverless, terminal := newHoldTest(1 << 20)

	before := startWrite(func() { logger.Raw("log 1\n") })
	requireBlocked(t, before)
	pause.holdOutput(func() {
		requireDone(t, before) // the waiting write is taken by the hold
		logger.Raw("log 2\n")
		_, _ = serverless.Write([]byte("\x1b[32mserverless 1\x1b[0m\n"))
		logger.Raw("log 3\n")
		_, _ = serverless.Write([]byte("serverless 2\n"))
		if got := terminal.String(); got != "" {
			t.Fatalf("terminal during the hold = %q", got)
		}
	})
	waitingLog := startWrite(func() { logger.Raw("log 4\n") })
	waitingGate := startWrite(func() { _, _ = serverless.Write([]byte("serverless 3\n")) })
	requireBlocked(t, waitingLog)
	requireBlocked(t, waitingGate)
	if got := terminal.String(); got != "" || !pause.paused || !pause.typing {
		t.Fatalf("after the hold: terminal %q paused %v typing %v", got, pause.paused, pause.typing)
	}

	pause.resume()
	requireDone(t, waitingLog)
	requireDone(t, waitingGate)
	want := "log 1\nlog 2\n\x1b[32mserverless 1\x1b[0m\nlog 3\nserverless 2\n"
	got := terminal.String()
	if !strings.HasPrefix(got, want) || len(got) != len(want)+len("log 4\nserverless 3\n") {
		t.Fatalf("terminal after the resume = %q, want %q first, then the two writes that waited", got, want)
	}
	pause.end()
	if pause.resume() || terminal.String() != got {
		t.Fatal("a second resume resumed or printed again")
	}
}

// TestHoldOutputBoundBlocks: once the hold is full the writers block during
// the wait as while paused, and what they write comes after the held text.
func TestHoldOutputBoundBlocks(t *testing.T) {
	pause, logger, serverless, terminal := newHoldTest(len("log 1\nserverless 1\n"))

	var blockedLog, blockedGate <-chan struct{}
	pause.holdOutput(func() {
		logger.Raw("log 1\n")
		_, _ = serverless.Write([]byte("serverless 1\n"))
		blockedLog = startWrite(func() { logger.Raw("log 2\n") })
		requireBlocked(t, blockedLog)
		blockedGate = startWrite(func() { _, _ = serverless.Write([]byte("s\n")) })
		requireBlocked(t, blockedGate)
	})
	if got := terminal.String(); got != "" {
		t.Fatalf("terminal with a full hold = %q", got)
	}
	pause.resume()
	requireDone(t, blockedLog)
	requireDone(t, blockedGate)
	if got := terminal.String(); !strings.HasPrefix(got, "log 1\nserverless 1\n") ||
		!strings.Contains(got, "log 2\n") || !strings.Contains(got, "s\n") {
		t.Fatalf("terminal = %q, want the held text first and the blocked writes after it", got)
	}
}

// TestHoldOutputSurvivesLaterWaits: text held by one wait stays through an
// explicit pause and a second wait and is printed once, in order, when the
// control loop ends (interactivePause.end, the normal shutdown), which
// waits until it is written.
func TestHoldOutputSurvivesLaterWaits(t *testing.T) {
	pause, logger, _, terminal := newHoldTest(1 << 20)

	pause.holdOutput(func() { logger.Raw("first wait\n") })
	pause.typing = false // :fields keeps the pause, now explicit
	pause.holdOutput(func() { logger.Raw("second wait\n") })
	if got := terminal.String(); got != "" {
		t.Fatalf("terminal while paused = %q", got)
	}
	// The end of the control loop resumes and waits for the held text.
	pause.end()
	if pause.paused || terminal.String() != "first wait\nsecond wait\n" {
		t.Fatalf("terminal after the end = %q", terminal.String())
	}
}

// explicitPauseTest is a client in an explicit pause (an empty line) whose
// logger streams numbered payload lines to terminal and whose server
// answers a table probe only once the logger took a payload line after
// mark writes, as a remote server's answer is queued behind payload.
type explicitPauseTest struct {
	mark     atomic.Int64
	c        *baseClient
	logger   *pausingTestLogger
	pause    *interactivePause
	terminal *orderedOutput
	tty      strings.Builder
}

func newExplicitPauseTest(t *testing.T) *explicitPauseTest {
	e := &explicitPauseTest{logger: newPausingTestLogger(), terminal: &orderedOutput{}}
	e.logger.out = e.terminal
	e.c = newPauseTestClient(e.logger)
	e.c.Args = queryArgs
	conn, handler := probeTestConnection("srv1")
	handler.tableGate = func() bool { return int64(e.logger.acceptedWrites()) > e.mark.Load() }
	e.c.connections = []connectors.Connector{conn}
	e.c.prober = e.c.newInteractiveProber()
	ctx, cancel := context.WithCancel(t.Context())
	streamed := make(chan struct{})
	t.Cleanup(func() { cancel(); e.pause.resume(); <-streamed; e.c.prober.wait() })
	e.c.prober.start(ctx)
	synctest.Wait()
	e.pause = e.c.newInteractivePause()
	go func() {
		defer close(streamed)
		for i := 0; ctx.Err() == nil; i++ {
			e.logger.Raw("payload " + strconv.Itoa(i) + "\n")
			time.Sleep(10 * time.Millisecond)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	e.line(t, "") // the explicit pause
	synctest.Wait()
	return e
}

func (e *explicitPauseTest) line(t *testing.T, line string) {
	t.Helper()
	if _, err := e.c.handleInteractiveLine(&e.tty, line, e.pause); err != nil {
		t.Fatal(err)
	}
}

// requireHeld requires that the writers went on since accepted writes
// while the terminal still shows printed and the pause is explicit.
func (e *explicitPauseTest) requireHeld(t *testing.T, what string, accepted int, printed string) {
	t.Helper()
	if e.logger.acceptedWrites() == accepted {
		t.Fatalf("%s: the payload's writer did not go on during the wait", what)
	}
	if got := e.terminal.String(); got != printed || !e.pause.paused || e.pause.typing {
		t.Fatalf("%s: terminal got %q during the explicit pause (paused %v typing %v)",
			what, strings.TrimPrefix(got, printed), e.pause.paused, e.pause.typing)
	}
}

// TestExplicitPauseHoldsOutputOfWaits: in an explicit pause, :fields TABLE
// (which waits for a table probe and keeps the output paused) and a Tab
// with a table probe print no payload, although its writer goes on during
// both waits; the held lines stay held until the empty line that resumes
// the output and come out then, in order and complete.
func TestExplicitPauseHoldsOutputOfWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newExplicitPauseTest(t)
		printed := e.terminal.String()

		accepted := e.logger.acceptedWrites()
		e.mark.Store(int64(accepted))
		e.line(t, ":fields orders")
		if !strings.Contains(e.tty.String(), "table ORDERS probed on its own") {
			t.Fatalf(":fields output = %q, want the table probe's answer", e.tty.String())
		}
		e.requireHeld(t, ":fields TABLE", accepted, printed)

		accepted = e.logger.acceptedWrites()
		e.mark.Store(int64(accepted))
		line := ":reload --query 'from STATS select "
		if got := e.c.completeEditorLine(e.pause, line, len(line)); strings.Contains(got.hint, "not answered") {
			t.Fatalf("Tab did not get the table probe's answer: %+v", got)
		}
		e.requireHeld(t, "Tab with a table probe", accepted, printed)

		time.Sleep(time.Second) // still held long after the waits
		if got := e.terminal.String(); got != printed {
			t.Fatalf("terminal got %q while explicitly paused", strings.TrimPrefix(got, printed))
		}
		e.line(t, "") // resume
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		got := e.terminal.String()
		if got == printed {
			t.Fatal("the held lines were not printed at the resume")
		}
		requireConsecutiveLines(t, got)
	})
}

// requireConsecutiveLines requires the "payload N" lines of text to be
// numbered from 0 on without a gap, a repeat or a swap.
func requireConsecutiveLines(t *testing.T, text string) {
	t.Helper()
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if line != "payload "+strconv.Itoa(i) {
			t.Fatalf("line %d is %q: lost, repeated or out of order", i, line)
		}
	}
}
