package clients

import (
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

// TestEditInputBurstPaste: in the query builder (joinPaste) a line break is
// the Enter key only when no input follows it, in the same read or within
// the quiet time; with input behind it, it is pasted text without paste
// markers, which is passed on as a bracketed paste of one line, ended once
// the terminal was quiet. Typed keys are untouched.
func TestEditInputBurstPaste(t *testing.T) {
	const start, end = "\x1b[200~", "\x1b[201~"
	heldEnter := slices.Repeat([]string{"\r"}, enterRepeatMax+3)
	tests := []struct {
		name   string
		script []string
		vi     bool
		want   string
	}{
		{name: "typed line and Enter", script: []string{"a", "b", "\r", quietGap}, want: "a|b|\r"},
		{name: "typed ahead line with its Enter", script: []string{"abc\r", quietGap, "d"}, want: "abc\r|d"},
		{name: "Enter, then typing after the quiet time", script: []string{"\r", quietGap, "x", "\r", quietGap},
			want: "\r|x|\r"},
		{name: "held Enter repeating within the quiet time", script: []string{"\r", "\r", "\r", quietGap},
			want: "\r|\r|\r"},
		{name: "held Enter repeating without a pause is passed on", script: append(heldEnter, quietGap),
			want: strings.Join(heldEnter, "|")},
		{name: "CR LF is one line end", script: []string{"a\r\n", quietGap}, want: "a\r\n"},
		{name: "lines in one read", script: []string{"from A\nselect b\necho c\n", quietGap, "\r", quietGap},
			want: start + "from A select b echo c |" + end + "|\r"},
		{name: "rest arrives within the quiet time", script: []string{"from A\n", "echo b\n", "echo c\n", quietGap},
			want: start + "from A echo b |echo c |" + end},
		{name: "blank lines then text", script: []string{"a\n\n", "\n", "b\n", quietGap},
			want: start + "a   b |" + end},
		{name: "control characters are blanks", script: []string{"a\n\tb\x1b[201~c\x03\n", quietGap},
			want: start + "a  b [201~c  |" + end},
		{name: "a character cut by a read", script: []string{"a\n\xc3", "\xa4\n", quietGap},
			want: start + "a |ä |" + end},
		{name: "typing goes on after the paste", script: []string{"a\nb", quietGap, "c", "\r", quietGap},
			want: start + "a b|" + end + "|c|\r"},
		{name: "vi: Esc and Enter typed", vi: true, script: []string{"\x1b", "dd", "\r", quietGap},
			want: string(viEscapeKey) + "|dd|\r"},
		{name: "vi: pasted lines", vi: true, script: []string{"a\nb\n", quietGap}, want: start + "a b |" + end},
		{name: "bracketed paste is left to its markers", script: []string{start + "a\nb" + end, "\r", quietGap},
			want: start + "a b" + end + "|\r"},
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
