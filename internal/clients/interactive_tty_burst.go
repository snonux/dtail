package clients

import (
	"bytes"
	"errors"
	"os"
	"time"
	"unicode/utf8"
)

// A terminal without bracketed paste (or one that has it switched off)
// sends pasted text as typed keys, so a pasted line break is the Enter key
// to the line editor. Elsewhere that only enters the pasted lines one by
// one. In the query builder of dmap (editInput.joinPaste) an entered line
// can end the process: the first pasted line that parses ran as the query,
// dmap exited, and the lines behind it, most of them not even written to
// the terminal's input queue yet (it holds 4096 bytes on Linux), were read
// and run by the shell. So there a line break with pasted text behind it
// (settle, pastedBehind) starts a "burst": pasted text without markers,
// passed on to the Terminal as a bracketed paste of one line (fillBurst),
// which stays on the edit line until Enter is typed. A burst means a
// paste, not fast typing: the one or two keys a person types within the
// window behind an Enter do not start one.
const (
	// enterQuietTime is the window behind a line break in which pasted
	// text must show (pastedBehind) for the line break not to be the Enter
	// key. A terminal writes a paste as fast as it is read, so its text
	// is there at once; a person types a key or two in that time.
	enterQuietTime = 30 * time.Millisecond
	// burstQuietTime is how long the terminal must be quiet for a burst to
	// have ended. It is longer than enterQuietTime, as it costs nothing to
	// wait once the input is known to be pasted, and a link (ssh) may
	// deliver a paste in pieces.
	burstQuietTime = 100 * time.Millisecond
	// burstMinBytes is how much text behind a line break is pasted for
	// sure: more than the keys typed within enterQuietTime, also than a
	// key held down (repeating about 30 times a second) or the sequence of
	// a function key (up to 7 bytes).
	burstMinBytes = 16
)

// readDeadliner is a TTY whose reads can be given a deadline (os.File where
// the runtime poller takes the terminal, as on Linux; kqueue refuses some
// terminal devices, e.g. /dev/tty on macOS, and SetReadDeadline fails).
type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

// readBy reads the TTY once if input arrives by deadline and reports
// whether it did. Where reads take no deadline (readDeadliner) it reports
// false at once: only what one read holds behind a line break tells a
// paste there. A read error other than the timeout is kept for Read.
func (in *editInput) readBy(deadline time.Time, size int) ([]byte, bool) {
	timed, ok := in.r.(readDeadliner)
	if !ok || timed.SetReadDeadline(deadline) != nil {
		return nil, false
	}
	defer func() { _ = timed.SetReadDeadline(time.Time{}) }()
	buf := make([]byte, size)
	n, err := in.r.Read(buf)
	if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		in.err = err
	}
	return buf[:n], n > 0
}

// pastedBehind reports whether tail, the input behind a line break, is
// pasted text and not keys typed after an Enter: at least burstMinBytes
// behind the line break (and the line breaks right behind it), or text
// that itself holds another line break (a second pasted line). Line breaks
// alone are the Enter key held down or pressed twice.
func pastedBehind(tail []byte) bool {
	text := bytes.TrimLeft(tail, "\r\n")
	return len(text) >= burstMinBytes || bytes.ContainsAny(text, "\r\n")
}

// settle decides, in the builder and outside a bracketed paste, what the
// first line break of data, just read, is. With pasted text behind it
// (pastedBehind) it starts a burst (see above), which data is then
// translated as. Otherwise it reads on for enterQuietTime, counted from
// here: input that arrives is added to data, which is looked at again.
// When the window ends without pasted text, the line break is the Enter
// key and what followed it are keys typed after it: data is returned as
// it is, and Read hands it out line by line, in order. Data that holds a
// paste start marker, also one that arrived in the window, is left to the
// bracketed paste.
func (in *editInput) settle(data []byte, size int) []byte {
	var window time.Time
	for {
		first := bytes.IndexAny(data, "\r\n")
		if first < 0 || bytes.Contains(data, pasteStart) {
			return data
		}
		if pastedBehind(data[first+1:]) {
			in.burst, in.pasting, in.pasteOut = true, true, true
			return data
		}
		if window.IsZero() {
			window = time.Now().Add(enterQuietTime)
		}
		more, arrived := in.readBy(window, size)
		if !arrived {
			return data
		}
		data = append(data, more...)
	}
}

// fillBurst is fill during a burst: input that arrives within
// burstQuietTime is more of the pasted text (its line breaks, control and
// format characters blanks, as in a bracketed paste; no paste marker in it
// is taken for one, translateAt); once the terminal was quiet that long
// (or the read failed) the burst ends with the paste end marker. The start
// of a character that a read cut stays held back for the read after the
// burst, which brings its rest (a link that stalled inside a character);
// only when the read failed, and nothing follows, it is passed on as it
// is.
func (in *editInput) fillBurst(size int) {
	in.pasteOut = true
	more, arrived := in.readBy(time.Now().Add(burstQuietTime), size)
	if !arrived {
		in.out = nil
		if in.err != nil || utf8.FullRune(in.held) {
			in.out, in.held = append(in.out, in.held...), nil
		}
		in.out = append(in.out, pasteEnd...)
		in.burst, in.pasting = false, false
		return
	}
	data := append(append([]byte(nil), in.held...), more...)
	in.held = nil
	in.out = in.translate(data, false)
}
