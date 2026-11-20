//go:build linux

package clients

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

// ptySettle is how long readPTYSettled reads: long enough for a redraw
// that should not happen to arrive.
const ptySettle = 300 * time.Millisecond

// readPTYSettled returns what the pseudo terminal master gets within
// ptySettle.
func readPTYSettled(t *testing.T, master *os.File) string {
	t.Helper()
	defer func() { _ = master.SetReadDeadline(time.Time{}) }()
	if err := master.SetReadDeadline(time.Now().Add(ptySettle)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var output strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := master.Read(buf)
		output.Write(buf[:n])
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return output.String()
		}
		if err != nil {
			t.Fatalf("read control TTY output %q: %v", output.String(), err)
		}
	}
}

// TestInteractiveEditorBracketedPasteOnPTY runs the control loop on a real
// pseudo terminal and pastes as a terminal in bracketed paste mode does,
// which the editor asks for at its start and gives back at its end. Pasted
// text that stays on the line pauses the output like typed text, and its
// tab completes nothing: it becomes a blank. The lines of a multi-line
// paste arrive as lines, each run as entered, and its blank lines are
// skipped: none toggles the output pause as the Enter key does.
func TestInteractiveEditorBracketedPasteOnPTY(t *testing.T) {
	t.Parallel()
	master, ttyPath := openTestPTY(t)
	slave, err := os.OpenFile(ttyPath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudo terminal slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	untilEnd := func(ctx context.Context) { <-ctx.Done() }
	c, _ := newSessionEndTestClient(t, untilEnd)
	logger := newPausingTestLogger()
	c.loggers.Client = logger
	c.Mode = omode.GrepClient
	c.What = "/var/log/app.log"
	c.RegexStr = "ERROR"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		_ = c.runInteractiveControl(ctx, cancel, slave, nil, testEditorDeps())
		done <- 0
	}()
	start := waitForPTYOutput(t, master, "Tab completes", done)
	if !strings.Contains(start, pasteModeOn) {
		t.Fatalf("editor did not ask for bracketed paste: %q", start)
	}

	// Typed, ":sh" plus Tab completes to ":show"; pasted, the tab is text.
	writePTY(t, master, "\x1b[200~:sh\tow\x1b[201~")
	out := waitForPTYOutput(t, master, ":sh ow", done)
	if !strings.Contains(out, interactiveTypingHint) || strings.Contains(out, "\t") {
		t.Fatalf("paste on the line: %q, want the typing hint and the tab as a blank", out)
	}
	waitForPauseDepth(t, logger, 1)
	writePTY(t, master, "\x15\n") // An empty line ends the typing pause.
	waitForPauseDepth(t, logger, 0)

	writePTY(t, master, "\x1b[200~\n:show\n\n  \n:quit\n\x1b[201~")
	out = waitForPTYOutput(t, master, "quitting interactive session", nil)
	if !strings.Contains(out, "mode=grep") {
		t.Fatalf("multi-line paste: %q, want :show run before :quit", out)
	}
	if strings.Contains(out, interactivePausedMessage) || strings.Contains(out, interactiveResumedMessage) {
		t.Fatalf("a blank line of the paste toggled the output pause: %q", out)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("control loop did not end after the pasted :quit")
	}
	if end := readPTYSettled(t, master); !strings.Contains(out+end, pasteModeOff) {
		t.Fatalf("editor left bracketed paste on at its end: %q", out+end)
	}
}

// TestInteractiveEditorDrawsDuringTabWaitOnPTY presses Tab on a real pseudo
// terminal where the completion waits for a server's path listing, and
// during that wait writes control output from another goroutine (the
// Ctrl-C statistics) and resizes the window (SIGWINCH). The line stays on
// the screen for the wait (the output is held, not printed), so both go
// through the Terminal as at any other time: the statistics are printed
// above the line, which is drawn again below them, the new width counts at
// once, and the completion then lands on the intact line.
func TestInteractiveEditorDrawsDuringTabWaitOnPTY(t *testing.T) {
	t.Parallel()
	master, ttyPath := openTestPTY(t)
	client, listingArrives := newGatedListingClient(t)
	lines, editor := openEditorOnPTY(t, client, ttyPath)
	read := make(chan string, 1)
	go func() {
		line, _ := lines.ReadLine()
		read <- line
	}()
	const typed = ":reload --files /srv/d"
	// The Tab on its own: the Terminal writes the echo of the keys of one
	// read only after it handled them all, the waiting Tab included.
	writePTY(t, master, typed)
	waitForPTYOutput(t, master, typed, nil)
	writePTY(t, master, "\t")
	handler := client.connections[0].Handler().(*probeTestHandler)
	waitFor(t, "the Tab's listing request", func() bool { return slices.Contains(handler.listed(), "/srv/") })
	readPTYSettled(t, master)

	if _, err := lines.Writer().Write([]byte("STATS during the wait\n")); err != nil {
		t.Fatal(err)
	}
	got := readPTYSettled(t, master)
	stats := strings.Index(got, "STATS during the wait")
	if stats < 0 || !strings.Contains(got[stats:], typed) {
		t.Fatalf("statistics during the Tab's wait: %q, want them printed and the line drawn again below", got)
	}
	setPTYColumns(t, ttyPath, 60)
	editor.resize() // what the SIGWINCH watcher calls
	if columns := editor.screen.columns(); columns != 60 {
		t.Fatalf("resize during the Tab's wait: %d columns, want 60 at once", columns)
	}

	listingArrives()
	waitForPTYOutput(t, master, "/srv/data.log", nil)
	writePTY(t, master, "\n")
	select {
	case line := <-read:
		if want := ":reload --files /srv/data.log"; line != want {
			t.Fatalf("line read = %q, want %q", line, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("line not read")
	}
	lines.close()
}

// openEditorOnPTY opens the client's line editor on the pseudo terminal
// at ttyPath, 100 columns wide.
func openEditorOnPTY(t *testing.T, client *baseClient, ttyPath string) (controlLines, *interactiveEditor) {
	t.Helper()
	slave, err := os.OpenFile(ttyPath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudo terminal slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	setPTYColumns(t, ttyPath, 100)
	lines := client.newControlLines(slave, client.newInteractivePause(), nil, testEditorDeps())
	editor, ok := lines.(*interactiveEditor)
	if !ok {
		t.Fatalf("control lines on a terminal = %T, want *interactiveEditor", lines)
	}
	return lines, editor
}

// newGatedListingClient returns a client whose server answers path
// listings only once the returned function was called, and whose Tab waits
// for one up to ten seconds.
func newGatedListingClient(t *testing.T) (*baseClient, func()) {
	t.Helper()
	client := newPauseTestClient(newPausingTestLogger())
	conn, handler := probeTestConnection("srv1")
	handler.listings = map[string][]protocol.ProbeEntry{"/srv/": {{Name: "data.log"}}}
	var open atomic.Bool
	handler.listGate = func(string) bool { return open.Load() }
	client.connections = []connectors.Connector{conn}
	client.prober = client.newInteractiveProber()
	client.prober.listWait = 10 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); client.prober.wait() })
	client.prober.start(ctx)
	return client, func() { open.Store(true) }
}

// setPTYColumns sets the window width of the pseudo terminal at path,
// through the slave's descriptor: Fd on the master would make it
// blocking, and its read deadlines would no longer hold.
func setPTYColumns(t *testing.T, path string, columns uint16) {
	t.Helper()
	size, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = size.Close() }()
	if err := unix.IoctlSetWinsize(int(size.Fd()), unix.TIOCSWINSZ,
		&unix.Winsize{Row: 30, Col: columns}); err != nil {
		t.Skipf("set window size: %v", err)
	}
}
