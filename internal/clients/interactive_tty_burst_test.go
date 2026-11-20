package clients

import (
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// quietGap in the script of a timedReader is a time without input that is
// longer than any quiet time waited for.
const quietGap = "<quiet>"

// timedReader is a TTY for editInput whose reads can have a deadline: it
// hands out the chunks of its script, one per read, and at a quietGap a
// read with a deadline times out (a read without one waits the gap out and
// returns what follows it).
type timedReader struct {
	script   []string
	deadline bool
}

func (r *timedReader) SetReadDeadline(deadline time.Time) error {
	r.deadline = !deadline.IsZero()
	return nil
}

func (r *timedReader) Read(p []byte) (int, error) {
	for len(r.script) > 0 && r.script[0] == quietGap {
		r.script = r.script[1:]
		if r.deadline {
			return 0, os.ErrDeadlineExceeded
		}
	}
	if len(r.script) == 0 {
		if r.deadline {
			return 0, os.ErrDeadlineExceeded
		}
		return 0, io.EOF
	}
	n := copy(p, r.script[0])
	if r.script[0] = r.script[0][n:]; r.script[0] == "" {
		r.script = r.script[1:]
	}
	return n, nil
}

// readAllInput reads in to its end, 256 bytes at a time as the Terminal
// does, and returns the reads joined with "|".
func readAllInput(in *editInput) string {
	var reads []string
	buf := make([]byte, 256)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			reads = append(reads, string(buf[:n]))
		}
		if err != nil {
			return strings.Join(reads, "|")
		}
	}
}

// TestEditInputBurstPaste: in the query builder (joinPaste) a line break
// with pasted text behind it (16 bytes, or another line, in the same read
// or within the window) is no Enter key: the text is passed on as a
// bracketed paste of one line, ended once the terminal was quiet. A burst
// means a paste, not fast typing: the keys a person types behind an Enter
// within the window are the Enter and those keys, in order.
func TestEditInputBurstPaste(t *testing.T) {
	const start, end = "\x1b[200~", "\x1b[201~"
	heldEnter := slices.Repeat([]string{"\r"}, 12)
	tests := []struct {
		name   string
		script []string
		vi     bool
		want   string
	}{
		{name: "typed line and Enter", script: []string{"a", "b", "\r", quietGap}, want: "a|b|\r"},
		{name: "typed ahead line with its Enter", script: []string{"abc\r", quietGap, "d"}, want: "abc\r|d"},
		{name: "Enter, then typing after the window", script: []string{"\r", quietGap, "x", "\r", quietGap},
			want: "\r|x|\r"},
		{name: "a key within the window of an Enter, then typing with Backspace and Enter",
			script: []string{":show\r", "f", quietGap, "r", quietGap, "x", quietGap, "\x7f", quietGap, "\x7f",
				quietGap, "\r", quietGap},
			want: ":show\r|f|r|x|\x7f|\x7f|\r"},
		{name: "keys behind an Enter in one read", script: []string{"abc\rde", quietGap}, want: "abc\r|de"},
		{name: "an arrow key behind an Enter", script: []string{"a\r", "\x1b[A", quietGap}, want: "a\r|\x1b[A"},
		{name: "Enter twice fast", script: []string{"\r", "\r", quietGap}, want: "\r|\r"},
		{name: "held Enter repeating within the window", script: append(heldEnter, quietGap),
			want: strings.Join(heldEnter, "|")},
		{name: "a held key repeating behind an Enter",
			script: []string{"\r", "x", quietGap, "x", quietGap, "x", quietGap}, want: "\r|x|x|x"},
		{name: "CR LF is one line end", script: []string{"a\r\n", quietGap}, want: "a\r\n"},
		{name: "16 bytes behind a line break are pasted", script: []string{"a\r0123456789abcdef", quietGap},
			want: start + "a 0123456789abcdef|" + end},
		{name: "15 bytes behind a line break are typed", script: []string{"a\r0123456789abcde", quietGap},
			want: "a\r|0123456789abcde"},
		{name: "lines in one read", script: []string{"from A\nselect b\necho c\n", quietGap, "\r", quietGap},
			want: start + "from A select b echo c |" + end + "|\r"},
		{name: "rest arrives within the window", script: []string{"from A\n", "echo b\n", "echo c\n", quietGap},
			want: start + "from A echo b |echo c |" + end},
		{name: "blank lines then a line", script: []string{"a\n\n", "\n", "b\n", quietGap},
			want: start + "a   b |" + end},
		{name: "control characters are blanks", script: []string{"a\n\tb\x1b[201~c\x03\n", quietGap},
			want: start + "a  b [201~c  |" + end},
		{name: "a character cut by a read", script: []string{"a\n\xc3", "\xa4\n", quietGap},
			want: start + "a ä |" + end},
		{name: "a character cut by the gap that ends the burst",
			script: []string{"a\nb\nc\xe2\x82", quietGap, "\xac", quietGap},
			want:   start + "a b c|" + end + "|€"},
		{name: "typing goes on after the paste", script: []string{"a\nb\nc", quietGap, "d", "\r", quietGap},
			want: start + "a b c|" + end + "|d|\r"},
		{name: "vi: Esc and Enter typed", vi: true, script: []string{"\x1b", "dd", "\r", quietGap},
			want: string(viEscapeKey) + "|dd|\r"},
		{name: "vi: Esc within the window of an Enter, then a normal mode key", vi: true,
			script: []string{":fields\r", "\x1b", quietGap, "k", quietGap},
			want:   ":fields\r|" + string(viEscapeKey) + "|k"},
		{name: "vi: pasted lines", vi: true, script: []string{"a\nb\n", quietGap}, want: start + "a b |" + end},
		{name: "bracketed paste is left to its markers", script: []string{start + "a\nb" + end, "\r", quietGap},
			want: start + "a b" + end + "|\r"},
		{name: "a bracketed paste within the window of an Enter",
			script: []string{":show\r", start + "hello\nworld" + end, quietGap, "\r", quietGap},
			want:   ":show\r|" + start + "hello world" + end + "|\r"},
		{name: "a bracketed paste cut by reads within the window of an Enter",
			script: []string{":show\r", "\x1b[20", "0~hello\nworld" + end, quietGap},
			want:   ":show\r|" + start + "hello world" + end},
		{name: "Alt-E", script: []string{"ab\x1be"}, want: "ab" + string(externalEditKey)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pastes := 0
			in := &editInput{r: &timedReader{script: tt.script}, joinPaste: true, viEscape: tt.vi,
				onPaste: func() { pastes++ }}
			if got := readAllInput(in); got != tt.want {
				t.Fatalf("read %q, want %q", got, tt.want)
			}
			if wantPaste := strings.Contains(tt.want, start); (pastes > 0) != wantPaste {
				t.Fatalf("onPaste called %d times, pasted text: %v", pastes, wantPaste)
			}
		})
	}
}

// TestEditInputBurstPasteOnlyInBuilder: outside the builder line breaks
// are entered as they come, and on a TTY whose reads take no deadline only
// what one read holds behind a line break tells a paste.
func TestEditInputBurstPasteOnlyInBuilder(t *testing.T) {
	in := &editInput{r: &timedReader{script: []string{"a\nb\n", "c\n"}}}
	if got := readAllInput(in); got != "a\n|b\n|c\n" {
		t.Fatalf("outside the builder: read %q", got)
	}
	in = &editInput{r: &chunkReader{chunks: []string{"a\n", "b\nc\n", "d"}}, joinPaste: true}
	if got := readAllInput(in); got != "a\n|\x1b[200~b c |\x1b[201~|d" {
		t.Fatalf("without deadlines: read %q", got)
	}
}

// TestEditInputBurstEndsWhenStopped is the regression test of a Ctrl-C
// during a paste: the burst's reads set and clear the read deadline
// themselves, so the deadline the cancellation set could lose against them
// for as long as the paste kept arriving, the control loop did not return
// within its wait, and the terminal was closed with the rest of the paste
// undrained. Once stop is closed the burst reads nothing more and Read
// returns the timeout the control loop takes as its end.
func TestEditInputBurstEndsWhenStopped(t *testing.T) {
	stop := make(chan struct{})
	paste := "from STATS select count($line)\n" + strings.Repeat("echo LEAKED\n", 4)
	reader := &timedReader{script: []string{paste, "more pasted text\n", "and more\n"}}
	in := &editInput{r: reader, joinPaste: true, stop: stop}
	buf := make([]byte, 256)
	if n, err := in.Read(buf); err != nil || n == 0 {
		t.Fatalf("first Read of the burst: %d bytes, %v", n, err)
	}
	close(stop)
	var err error
	for i := 0; i < 8 && err == nil; i++ {
		_, err = in.Read(buf)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read after the stop: %v, want the timeout that ends the control loop", err)
	}
	if len(reader.script) != 2 {
		t.Fatalf("the stopped burst read on: %d chunks left, want the 2 behind the first", len(reader.script))
	}
}
