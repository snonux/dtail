package loggers

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// testHold takes held output like the hold of an interactive client: up to
// max bytes, in order, each text with the writer it goes back to.
type testHold struct {
	mu     sync.Mutex
	max    int
	size   int
	chunks []testHeld
}

type testHeld struct {
	w    io.Writer
	text string
}

func (h *testHold) take(w io.Writer, p []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.size+len(p) > h.max {
		return false
	}
	h.size += len(p)
	h.chunks = append(h.chunks, testHeld{w: w, text: string(p)})
	return true
}

func (h *testHold) held() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.chunks)
}

// release writes the held texts back in order.
func (h *testHold) release() {
	h.mu.Lock()
	chunks := h.chunks
	h.chunks, h.size = nil, 0
	h.mu.Unlock()
	for _, chunk := range chunks {
		_, _ = io.WriteString(chunk.w, chunk.text)
	}
}

// logAsync logs in a goroutine and returns a channel closed once the call
// returned.
func logAsync(log func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		log()
	}()
	return done
}

func requireReturned(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return", what)
	}
}

func requireWaiting(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("%s returned while the logger was paused", what)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestStdoutHoldsOutputWhilePaused: while the paused logger holds its
// output, log calls of every kind return at once and nothing reaches the
// terminal; the hold gives the text back unchanged (colors included) and
// in order before the pause ends, and what is logged after the holding
// ended waits for the resume and comes after it.
func TestStdoutHoldsOutputWhilePaused(t *testing.T) {
	out := &countingWriter{}
	s := newStdoutWriter(out)
	hold := &testHold{max: 1 << 20}

	s.Log("before")
	s.Pause()
	s.HoldOutput(hold.take)
	done := logAsync(func() {
		s.Log("diagnostic")
		s.LogWithColors("", "\x1b[31mcolored\x1b[0m")
		s.Raw("payload\n")
		s.RawWithColors("", "\x1b[32mpayload\x1b[0m\n")
		s.RawBytes([]byte("bytes\n"))
	})
	requireReturned(t, "logging while held", done)
	if got := out.String(); got != "before\n" {
		t.Fatalf("terminal while held = %q, want only what was logged before the pause", got)
	}
	s.HoldOutput(nil)
	waiting := logAsync(func() { s.Log("after the hold") })
	requireWaiting(t, "a log call after the holding ended", waiting)

	hold.release()
	want := "before\ndiagnostic\n\x1b[31mcolored\x1b[0m\npayload\n\x1b[32mpayload\x1b[0m\nbytes\n"
	if got := out.String(); got != want {
		t.Fatalf("terminal after the release = %q, want %q", got, want)
	}
	s.Resume()
	requireReturned(t, "the waiting log call", waiting)
	s.Flush()
	if got := out.String(); got != want+"after the hold\n" {
		t.Fatalf("terminal after the resume = %q", got)
	}
}

// TestStdoutHoldBoundBlocks: text the hold refuses (it is full) waits for
// the pause to end as without a hold, also one already waiting when the
// holding starts is offered to it, and comes after the held text.
func TestStdoutHoldBoundBlocks(t *testing.T) {
	out := &countingWriter{}
	s := newStdoutWriter(out)
	hold := &testHold{max: len("first\n")}

	s.Pause()
	waiting := logAsync(func() { s.Log("first") })
	requireWaiting(t, "a log call of the paused logger", waiting)
	s.HoldOutput(hold.take)
	requireReturned(t, "the waiting log call once the logger holds", waiting)
	refused := logAsync(func() { s.Log("second") })
	requireWaiting(t, "a log call the full hold refused", refused)
	if out.String() != "" || hold.held() != 1 {
		t.Fatalf("terminal %q, %d texts held; want nothing printed and one held", out.String(), hold.held())
	}

	s.HoldOutput(nil)
	hold.release()
	s.Resume()
	requireReturned(t, "the refused log call", refused)
	s.Flush()
	if got := out.String(); got != "first\nsecond\n" {
		t.Fatalf("terminal = %q, want the held line before the one that waited", got)
	}
}

// TestStdoutHeldOutputRefusedUnderOtherPausers: held text given back
// while another pauser (the host key question, the Ctrl-C statistics) has
// the logger paused too is not written: the write is refused with an
// error marked Paused, without waiting, so that the text does not run into
// what that pauser shows and its owner keeps it, in order. Once only the
// holding pauser is left it is written, before the log calls that waited.
func TestStdoutHeldOutputRefusedUnderOtherPausers(t *testing.T) {
	out := &countingWriter{}
	s := newStdoutWriter(out)
	var held io.Writer
	take := func(w io.Writer, _ []byte) bool { held = w; return true }

	s.Pause() // the pauser that holds
	s.HoldOutput(take)
	s.Log("A")
	s.HoldOutput(nil)
	s.Pause() // another one
	waiting := logAsync(func() { s.Log("waited") })
	n, err := held.Write([]byte("A\n"))
	var paused interface{ Paused() bool }
	if n != 0 || !errors.As(err, &paused) || !paused.Paused() || out.String() != "" {
		t.Fatalf("held text under another pauser: n %d err %v terminal %q, want it refused as paused",
			n, err, out.String())
	}
	s.Resume() // the other pauser
	if n, err := held.Write([]byte("A\n")); n != 2 || err != nil || out.String() != "A\n" {
		t.Fatalf("held text with only its pauser left: n %d err %v terminal %q", n, err, out.String())
	}
	requireWaiting(t, "a log call while the holding pauser pauses", waiting)
	s.Resume() // the pauser that held
	requireReturned(t, "the waiting log call", waiting)
	s.Flush()
	if got := out.String(); got != "A\nwaited\n" {
		t.Fatalf("terminal = %q, want the held line, then the one that waited", got)
	}
}

// TestStdoutHeldOutputGoesThroughRedirect: held text of a redirected
// logger (the query builder's line editor) comes back through the
// redirect.
func TestStdoutHeldOutputGoesThroughRedirect(t *testing.T) {
	own, editor := &countingWriter{}, &countingWriter{}
	s := newStdoutWriter(own)
	hold := &testHold{max: 1 << 20}

	s.RedirectOutput(editor)
	s.Pause()
	s.HoldOutput(hold.take)
	s.Log("connection error")
	s.HoldOutput(nil)
	if editor.String() != "" {
		t.Fatalf("editor got %q while held", editor.String())
	}
	hold.release()
	s.Resume()
	if editor.String() != "connection error\n" || own.String() != "" {
		t.Fatalf("editor %q, own %q; want the held line in the editor", editor.String(), own.String())
	}
}

// textSink is a file sink that records what it gets and never pauses.
type textSink struct {
	mu   sync.Mutex
	text string
}

func (s *textSink) add(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text += text
}

func (s *textSink) Log(message string)              { s.add(message + "\n") }
func (s *textSink) LogWithColors(message, _ string) { s.add(message + "\n") }
func (s *textSink) Raw(message string)              { s.add(message) }
func (s *textSink) RawWithColors(message, _ string) { s.add(message) }
func (s *textSink) Flush()                          {}
func (s *textSink) SupportsColors() bool            { return false }

func (s *textSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text
}

// TestFoutHoldsItsTerminalHalfOnly: while the default client logger holds
// its output, the file gets every diagnostic (and opted-in payload) at
// once, the terminal nothing until the release.
func TestFoutHoldsItsTerminalHalfOnly(t *testing.T) {
	file, terminal := &textSink{}, &countingWriter{}
	f := newFoutWithSinks(file, newStdoutWriter(terminal), true)
	hold := &testHold{max: 1 << 20}

	f.Pause()
	f.HoldOutput(hold.take)
	done := logAsync(func() {
		f.Log("diagnostic")
		f.Raw("payload\n")
		f.RawBytes([]byte("bytes\n"))
	})
	requireReturned(t, "logging while held", done)
	if file.String() != "diagnostic\npayload\nbytes\n" || terminal.String() != "" {
		t.Fatalf("file %q, terminal %q; want the file written and the terminal held",
			file.String(), terminal.String())
	}
	f.HoldOutput(nil)
	hold.release()
	f.Resume()
	if terminal.String() != "diagnostic\npayload\nbytes\n" {
		t.Fatalf("terminal after the release = %q", terminal.String())
	}

	// A logger without a terminal half takes the hold without effect.
	newFoutWithSinks(&recordingSink{}, &recordingSink{}, false).HoldOutput(hold.take)
}
