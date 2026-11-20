package loggers

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestStdoutRedirectOutput: a redirected logger writes to the given writer,
// what was buffered before goes to the output it was logged for, and a nil
// writer gives the logger its own output back.
func TestStdoutRedirectOutput(t *testing.T) {
	own, editor := &countingWriter{}, &countingWriter{}
	s := newStdoutWriter(own)

	s.Log("before")
	s.RedirectOutput(editor)
	if got := own.String(); got != "before\n" {
		t.Fatalf("own output at the redirect = %q, want the line buffered before it", got)
	}
	s.Log("during")
	s.Raw("payload\n")
	s.Flush()
	if got := editor.String(); got != "during\npayload\n" {
		t.Fatalf("redirected output = %q", got)
	}

	s.Log("held")
	s.RedirectOutput(nil)
	if got := editor.String(); got != "during\npayload\nheld\n" {
		t.Fatalf("redirected output when it ended = %q, want what was buffered for it", got)
	}
	s.Log("after")
	s.Flush()
	if got := own.String(); got != "before\nafter\n" {
		t.Fatalf("own output after the redirect = %q", got)
	}
}

// TestFoutRedirectsItsTerminalHalfOnly: the file of the default client
// logger keeps every line while the terminal output is redirected, and a
// logger without terminal output takes the redirect without effect.
func TestFoutRedirectsItsTerminalHalfOnly(t *testing.T) {
	file, own, editor := &countingWriter{}, &countingWriter{}, &countingWriter{}
	fileSink := newStdoutWriter(file)
	f := newFoutWithSinks(fileSink, newStdoutWriter(own), false)

	f.RedirectOutput(editor)
	f.Log("line")
	f.Flush()
	if editor.String() != "line\n" || file.String() != "line\n" || own.String() != "" {
		t.Fatalf("editor %q, file %q, own %q; want the line in the editor and the file",
			editor.String(), file.String(), own.String())
	}
	f.RedirectOutput(nil)
	f.Log("next")
	f.Flush()
	if own.String() != "next\n" || editor.String() != "line\n" {
		t.Fatalf("own %q, editor %q after the redirect ended", own.String(), editor.String())
	}

	// A sink that is no terminal logger is left alone.
	plain := newFoutWithSinks(fileSink, none{}, false)
	plain.RedirectOutput(editor)
}

// lineCheckingWriter records every write and whether one did not end with
// a line break.
type lineCheckingWriter struct {
	writes  [][]byte
	partial int
}

func (w *lineCheckingWriter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, append([]byte(nil), p...))
	if len(p) == 0 || p[len(p)-1] != '\n' {
		w.partial++
	}
	return len(p), nil
}

func (w *lineCheckingWriter) String() string {
	return string(bytes.Join(w.writes, nil))
}

// TestStdoutRedirectWritesWholeLines: the redirect target gets whole lines
// only, also when the logger's buffer fills in the middle of a line and
// flushes itself (the line editor draws its prompt again below every
// write); nothing is lost or reordered, and text without a line break is
// passed on when its line ends or the redirect does.
func TestStdoutRedirectWritesWholeLines(t *testing.T) {
	own, editor := &countingWriter{}, &lineCheckingWriter{}
	s := newStdoutWriter(own)
	s.RedirectOutput(editor)
	var want strings.Builder
	line := strings.Repeat("x", 999)
	// 300 lines of 1000 bytes: the 64 KiB buffer fills several times, each
	// time in the middle of a line.
	for i := range 300 {
		s.Log(fmt.Sprintf("%03d %s", i, line[4:]))
		fmt.Fprintf(&want, "%03d %s\n", i, line[4:])
	}
	s.Flush()
	if editor.partial != 0 {
		t.Fatalf("%d of %d writes to the redirect target did not end with a line", editor.partial, len(editor.writes))
	}
	if len(editor.writes) < 3 {
		t.Fatalf("%d writes, want the buffer to have flushed itself several times", len(editor.writes))
	}
	if editor.String() != want.String() {
		t.Fatal("the redirected output is not the lines logged, in order")
	}

	// Text without a line break waits for its line's end ...
	s.Raw("begin ")
	s.Flush()
	if editor.String() != want.String() {
		t.Fatalf("an unfinished line was written: %q", editor.String()[want.Len():])
	}
	s.Raw("end\n")
	s.Flush()
	want.WriteString("begin end\n")
	if editor.String() != want.String() || editor.partial != 0 {
		t.Fatalf("the finished line was not written whole: %q", editor.String()[want.Len()-10:])
	}
	// ... or for the end of the redirect, and does not reach the logger's
	// own output.
	s.Raw("unfinished")
	s.RedirectOutput(nil)
	if got := editor.String(); got != want.String()+"unfinished" {
		t.Fatalf("the text held at the end of the redirect was not written to it: %q", got[want.Len():])
	}
	if own.String() != "" {
		t.Fatalf("own output = %q, want nothing", own.String())
	}
}

// TestWholeLinesPassesOnALineLongerThanTheBuffer: a line that can not be
// kept whole is passed on rather than held without bound.
func TestWholeLinesPassesOnALineLongerThanTheBuffer(t *testing.T) {
	target := &lineCheckingWriter{}
	lines := &wholeLines{w: target}
	chunk := bytes.Repeat([]byte("y"), stdoutWriterBufSize/2+1)
	if n, err := lines.Write(chunk); n != len(chunk) || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(target.writes) != 0 {
		t.Fatalf("%d writes before the line outgrew the buffer", len(target.writes))
	}
	if n, err := lines.Write(chunk); n != len(chunk) || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := len(target.String()); got != 2*len(chunk) || len(lines.tail) != 0 {
		t.Fatalf("passed on %d bytes, kept %d, want all %d passed on", got, len(lines.tail), 2*len(chunk))
	}
}
