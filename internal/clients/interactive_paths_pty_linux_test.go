//go:build linux

package clients

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog/loggers"

	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

// listingPTYHandler is the connection handler of the path completion PTY
// test: a session that never ends and answers a path listing only once the
// logger took a payload line after the request (printed or held), as a
// remote server's answer frame queued behind payload is read only once the
// output takes that payload (its field probes get an empty answer).
type listingPTYHandler struct {
	*statusTestHandler
	open chan struct{}
	// accepted counts the payload writes the logger took; a listing is
	// answered once it took need more (one when zero).
	accepted func() int
	need     int
	listings map[string][]protocol.ProbeEntry

	mu     sync.Mutex
	listed []string
}

func (h *listingPTYHandler) Done() <-chan struct{} { return h.open }

func (h *listingPTYHandler) Probe(ctx context.Context, request protocol.ProbeRequest) (protocol.ProbeAnswer, error) {
	if request.List == "" {
		return protocol.ProbeAnswer{ID: request.ID}, nil
	}
	until := h.accepted() + max(h.need, 1)
	for h.accepted() < until {
		select {
		case <-ctx.Done():
			return protocol.ProbeAnswer{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	h.mu.Lock()
	h.listed = append(h.listed, request.List)
	h.mu.Unlock()
	return protocol.ProbeAnswer{ID: request.ID, Entries: h.listings[request.List]}, nil
}

func (h *listingPTYHandler) listedPrefixes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.listed)
}

// ptyCapture collects everything a pseudo terminal's master gets.
type ptyCapture struct {
	mu  sync.Mutex
	out strings.Builder
}

// capturePTY reads master in the background until it is closed.
func capturePTY(master *os.File) *ptyCapture {
	c := &ptyCapture{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			c.mu.Lock()
			c.out.Write(buf[:n])
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *ptyCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

// waitFor waits until the output from offset from on contains want and
// returns the offset behind it.
func (c *ptyCapture) waitFor(t *testing.T, from int, want string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		out := c.String()
		if i := strings.Index(out[from:], want); i >= 0 {
			return from + i + len(want)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no terminal output %q within 10s (got %q)", want, out[from:])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// requireConsecutivePayload requires the "payload N" lines of text to be
// numbered from first on without a gap, a repeat or a swap.
func requireConsecutivePayload(t *testing.T, text string, first int) int {
	t.Helper()
	next := first
	for _, line := range strings.Split(text, "\n") {
		i := strings.Index(line, "payload ")
		if i < 0 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimRight(line[i+len("payload "):], "\r"))
		if err != nil || n != next {
			t.Fatalf("payload line %q, want number %d: lost, repeated or out of order", line, next)
		}
		next++
	}
	return next
}

// TestInteractiveEditorCompletesPathsOnPTY runs the line editor on a real
// pseudo terminal while numbered payload lines stream to that terminal:
// Tab in a --files value completes a file of the prefetched directory at
// once, and a file of a directory not listed yet after waiting for its
// listing, which the server answers only once the client took more payload.
// During that wait the payload's writer goes on, but no payload line
// reaches the terminal, neither then nor until the line is entered: the
// held lines are printed when the entered line resumes the output, after
// its answer, in order and complete. The same in a line with the prompt
// (explicit pause), whose held lines the end of the session prints.
func TestInteractiveEditorCompletesPathsOnPTY(t *testing.T) {
	t.Parallel()
	p := startPathsPTYTest(t)
	typing := p.completeCached(t)
	completed := p.completeAfterWait(t, typing)
	resumed := p.enterLine(t, completed)
	p.completeInExplicitPause(t, resumed)
	p.quit(t)
}

// pathsPTYTest is an interactive client on a pseudo terminal whose logger
// streams numbered payload lines to that terminal.
type pathsPTYTest struct {
	master *os.File
	logger *pausingTestLogger
	screen *ptyCapture
	done   chan int
}

// startPathsPTYTest starts the client and waits until its control loop
// runs and the directory of its file was listed.
func startPathsPTYTest(t *testing.T) *pathsPTYTest {
	t.Helper()
	master, ttyPath := openTestPTY(t)
	terminal, err := os.OpenFile(ttyPath, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudo terminal slave: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	logger := newPausingTestLogger()
	logger.out = terminal
	stream := func(ctx context.Context) {
		for i := 0; ctx.Err() == nil; i++ {
			logger.Raw("payload " + strconv.Itoa(i) + "\n")
			time.Sleep(2 * time.Millisecond)
		}
	}
	c, _ := newSessionEndTestClient(t, stream)
	handler := &listingPTYHandler{statusTestHandler: &statusTestHandler{}, open: make(chan struct{}),
		accepted: logger.acceptedWrites, listings: map[string][]protocol.ProbeEntry{
			"/var/log/": {{Name: "app.log"}}, "/srv/": {{Name: "data.log"}}, "/opt/": {{Name: "other.log"}}}}
	c.connections[0].(*finalTestConnector).handler = handler
	c.loggers.Client = logger
	c.InteractiveQuery = true
	c.ControlTTYPath = ttyPath
	c.Mode = omode.GrepClient
	c.What = "/var/log/app.log"
	c.RegexStr = "ERROR"
	c.sessionSpec = NewSessionSpec(c.Args) // the prober reads the files from it

	p := &pathsPTYTest{master: master, logger: logger, screen: capturePTY(master), done: make(chan int, 1)}
	go func() { p.done <- c.Start(context.Background(), nil) }()
	p.screen.waitFor(t, 0, "Tab completes")
	waitFor(t, "prefetched listing", func() bool { return slices.Contains(handler.listedPrefixes(), "/var/log/") })
	return p
}

// completeCached starts a line (pausing the output for typing) and
// completes a file of the prefetched directory: no payload write goes
// through. It returns the screen offset of the typing hint.
func (p *pathsPTYTest) completeCached(t *testing.T) int {
	t.Helper()
	writePTY(t, p.master, ":")
	typing := p.screen.waitFor(t, 0, interactiveTypingHint)
	waitForPauseDepth(t, p.logger, 1)
	paused := len(p.logger.writtenLines())
	writePTY(t, p.master, "reload --files /var/log/a\t")
	p.screen.waitFor(t, typing, "/var/log/app.log")
	if written := len(p.logger.writtenLines()); written != paused {
		t.Fatalf("cached completion let %d payload writes through", written-paused)
	}
	return typing
}

// completeAfterWait completes a file of a directory not listed yet: the
// payload's writer goes on during the Tab's wait, but nothing of it
// reaches the terminal, then or after. It returns the screen offset behind
// the completed line.
func (p *pathsPTYTest) completeAfterWait(t *testing.T, typing int) int {
	t.Helper()
	paused := len(p.logger.writtenLines())
	accepted := p.logger.acceptedWrites()
	writePTY(t, p.master, "\x15:reload --files /srv/d\t")
	completed := p.screen.waitFor(t, typing, "/srv/data.log")
	if p.logger.acceptedWrites() == accepted {
		t.Fatal("the payload's writer did not go on while Tab waited for the listing")
	}
	time.Sleep(50 * time.Millisecond) // nothing follows the wait either
	if written := len(p.logger.writtenLines()); written != paused || p.logger.pauseDepth() != 1 {
		t.Fatalf("%d payload writes reached the terminal during the Tab's wait (pause depth %d)",
			written-paused, p.logger.pauseDepth())
	}
	if out := p.screen.String()[typing:]; strings.Contains(out, "payload ") {
		t.Fatalf("payload on the terminal between the typing and the entered line: %q", out)
	}
	return completed
}

// enterLine enters a line: it is answered, then the output resumes, the
// held lines first. It returns the screen offset of the first payload.
func (p *pathsPTYTest) enterLine(t *testing.T, completed int) int {
	t.Helper()
	writePTY(t, p.master, "\x15:show\n")
	answered := p.screen.waitFor(t, completed, "workload=")
	waitForPauseDepth(t, p.logger, 0)
	resumed := p.screen.waitFor(t, answered, "payload ")
	if out := p.screen.String()[completed:answered]; strings.Contains(out, "payload ") {
		t.Fatalf("held payload printed before the entered line's answer: %q", out)
	}
	return resumed
}

// completeInExplicitPause does the same in a line with the prompt (started
// in an explicit pause), and leaves its held lines to the session's end.
func (p *pathsPTYTest) completeInExplicitPause(t *testing.T, resumed int) {
	t.Helper()
	writePTY(t, p.master, "\n")
	prompt := p.screen.waitFor(t, resumed, "interactive-query.md).\r\r\n"+interactivePrompt)
	waitForPauseDepth(t, p.logger, 1)
	paused := len(p.logger.writtenLines())
	accepted := p.logger.acceptedWrites()
	writePTY(t, p.master, ":reload --files /opt/o\t")
	p.screen.waitFor(t, prompt, "/opt/other.log")
	if written := len(p.logger.writtenLines()); written != paused || p.logger.acceptedWrites() == accepted {
		t.Fatalf("explicit pause: %d payload writes on the terminal, %d taken during the wait",
			written-paused, p.logger.acceptedWrites()-accepted)
	}
	if out := p.screen.String()[prompt:]; strings.Contains(out, "payload ") {
		t.Fatalf("payload on the terminal in the explicit pause: %q", out)
	}
}

// quit ends the session, which prints the lines still held: every payload
// line the logger took is on the terminal, in order. One write waits at
// the pause while the others are held, so with the held ones lost or
// printed late the numbers would have a gap or a swap.
func (p *pathsPTYTest) quit(t *testing.T) {
	t.Helper()
	writePTY(t, p.master, "\x15:quit\n")
	requireStartStatus(t, p.done, 0)
	printed := requireConsecutivePayload(t, strings.Join(p.logger.writtenLines(), ""), 0)
	if printed < p.logger.acceptedWrites() {
		t.Fatalf("%d payload lines printed, %d taken: held lines lost at the session's end",
			printed, p.logger.acceptedWrites())
	}
	waitFor(t, "the last payload line on the terminal", func() bool {
		return strings.Contains(p.screen.String(), "payload "+strconv.Itoa(printed-1)+"\r\n")
	})
	requireConsecutivePayload(t, p.screen.String(), 0)
}

// countingTerminalLogger is the real terminal logger writing to a pseudo
// terminal, counting the payload writes it took.
type countingTerminalLogger struct {
	terminalTestLogger
	accepted atomic.Int64
}

func (l *countingTerminalLogger) Raw(message string) string {
	l.out.Raw(message)
	l.accepted.Add(1)
	return message
}

// startTypeAheadPTYTest starts an interactive client on a pseudo terminal
// whose real terminal logger streams numbered payload lines of about 200
// bytes to that terminal, and whose server answers a path listing only
// after 300 more of them were taken, so that a Tab's wait holds about
// 60 KiB.
func startTypeAheadPTYTest(t *testing.T) (*os.File, *ptyCapture, *countingTerminalLogger, chan int) {
	t.Helper()
	master, ttyPath := openTestPTY(t)
	terminal, err := os.OpenFile(ttyPath, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudo terminal slave: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	logger := &countingTerminalLogger{terminalTestLogger: terminalTestLogger{out: loggers.NewTerminal(terminal)}}
	pad := strings.Repeat("x", 190)
	stream := func(ctx context.Context) {
		for i := 0; ctx.Err() == nil; i++ {
			logger.Raw(pad + " payload " + strconv.Itoa(i) + "\n")
			logger.out.Flush()
			time.Sleep(20 * time.Microsecond)
		}
	}
	c, _ := newSessionEndTestClient(t, stream)
	handler := &listingPTYHandler{statusTestHandler: &statusTestHandler{}, open: make(chan struct{}),
		accepted: func() int { return int(logger.accepted.Load()) }, need: 300,
		listings: map[string][]protocol.ProbeEntry{"/var/log/": {{Name: "app.log"}}, "/srv/": {{Name: "data.log"}}}}
	c.connections[0].(*finalTestConnector).handler = handler
	c.loggers.Client = logger
	c.InteractiveQuery = true
	c.ControlTTYPath = ttyPath
	c.Mode = omode.GrepClient
	c.What = "/var/log/app.log"
	c.RegexStr = "ERROR"
	c.sessionSpec = NewSessionSpec(c.Args)

	screen := capturePTY(master)
	done := make(chan int, 1)
	go func() { done <- c.Start(context.Background(), nil) }()
	screen.waitFor(t, 0, "Tab completes")
	waitFor(t, "prefetched listing", func() bool { return slices.Contains(handler.listedPrefixes(), "/var/log/") })
	return master, screen, logger, done
}

// TestTypeAheadBehindEnterStopsHeldOutputOnPTY: a Tab's wait holds a large
// backlog, the line is entered and the first key of the next line arrives
// in the same read, right behind the Enter, while the held text is being
// written (real control loop, real terminal logger, pseudo terminal).
// From that key's typing hint on, nothing of the held text may reach the
// terminal until the next line is entered; then all of it arrives, in
// order and complete.
func TestTypeAheadBehindEnterStopsHeldOutputOnPTY(t *testing.T) {
	t.Parallel()
	master, screen, logger, done := startTypeAheadPTYTest(t)

	writePTY(t, master, ":reload --files /srv/d")
	typing := screen.waitFor(t, 0, interactiveTypingHint)
	writePTY(t, master, "\t")
	completed := screen.waitFor(t, typing, "/srv/data.log")
	// Enter and the next line's first keys in one write.
	writePTY(t, master, "\x15:show\n:sho")
	second := screen.waitFor(t, completed, interactiveTypingHint)
	time.Sleep(300 * time.Millisecond)
	out := screen.String()
	if strings.Contains(out[second:], "payload ") {
		t.Fatalf("%d held lines reached the terminal while the next line was typed: %q",
			strings.Count(out[second:], "payload "), out[second:min(len(out), second+600)])
	}
	if printed, taken := strings.Count(out, "payload "), int(logger.accepted.Load()); printed >= taken {
		t.Fatalf("%d lines printed of %d taken: no held text was left to stop, the test proves nothing",
			printed, taken)
	}

	writePTY(t, master, "w\n")
	screen.waitFor(t, second, "workload=")
	screen.waitFor(t, second, "payload ")
	writePTY(t, master, ":quit\n")
	select {
	case status := <-done:
		if status != 0 {
			t.Fatalf("Start status = %d, want 0", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("interactive client did not end")
	}
	last := int(logger.accepted.Load()) - 1
	waitFor(t, "the last payload line on the terminal", func() bool {
		return strings.Contains(screen.String(), "payload "+strconv.Itoa(last)+"\r\n")
	})
	requireConsecutivePayload(t, screen.String(), 0)
}

// requireStartStatus waits up to 10 s for the status of the client's Start.
func requireStartStatus(t *testing.T, done <-chan int, want int) {
	t.Helper()
	select {
	case status := <-done:
		if status != want {
			t.Fatalf("Start status = %d, want %d", status, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("interactive client did not end")
	}
}

func writePTY(t *testing.T, master *os.File, s string) {
	t.Helper()
	if _, err := master.WriteString(s); err != nil {
		t.Fatalf("write %q: %v", s, err)
	}
}

// waitFor polls cond for up to 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
