package loggers

import (
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
