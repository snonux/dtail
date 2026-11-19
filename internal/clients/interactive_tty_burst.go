package clients

import (
	"bytes"
	"errors"
	"os"
	"time"
)

// A terminal without bracketed paste (or one that has it switched off)
// sends pasted text as typed keys, so a pasted line break is the Enter key
// to the line editor. Elsewhere that only enters the pasted lines one by
// one. In the query builder of dmap (editInput.joinPaste) an entered line
// can end the process: the first pasted line that parses ran as the query,
// dmap exited, and the lines behind it, most of them not even written to
// the terminal's input queue yet (it holds 4096 bytes on Linux), were read
// and run by the shell. So there a line break is the Enter key only when no
// input follows it (settle); a line break with input behind it starts a
// "burst": pasted text without markers, passed on to the Terminal as a
// bracketed paste of one line (fillBurst), which stays on the edit line
// until Enter is typed.
const (
	// enterQuietTime is how long no input may follow a line break for it to
	// be the Enter key: far below what a person needs for the next key
	// after Enter, above the pause between the pieces of a paste (a
	// terminal writes them as fast as they are read).
	enterQuietTime = 30 * time.Millisecond
	// burstQuietTime is how long the terminal must be quiet for a burst to
	// have ended. It is longer than enterQuietTime, as it costs nothing to
	// wait once the input is known to be pasted, and a link (ssh) may
	// deliver a paste in pieces.
	burstQuietTime = 100 * time.Millisecond
	// enterRepeatMax bounds how often settle waits for more input after
	// line breaks that only more line breaks followed (a held Enter key
	// repeating faster than enterQuietTime): they are then passed on as
	// they are, so the editor does not stop answering while Enter is held.
	enterRepeatMax = 8
)

// readDeadliner is a TTY whose reads can be given a deadline (os.File where
// the runtime poller takes the terminal, as on Linux; kqueue refuses some
// terminal devices, e.g. /dev/tty on macOS, and SetReadDeadline fails).
type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

// readWithin reads the TTY once if input arrives within quiet and reports
// whether it did. Where reads take no deadline (readDeadliner) it reports
// false at once: only what one read holds behind a line break tells a
// paste there. A read error other than the timeout is kept for Read.
func (in *editInput) readWithin(quiet time.Duration, size int) ([]byte, bool) {
	timed, ok := in.r.(readDeadliner)
	if !ok || timed.SetReadDeadline(time.Now().Add(quiet)) != nil {
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

// settle decides, in the builder and outside a bracketed paste, what the
// first line break of data, just read, is. With anything but line breaks
// behind it in data it starts a burst (see above), which data is then
// translated as. With nothing behind it, it waits enterQuietTime for more
// input: none makes it the Enter key, and data is returned as it is; input
// that arrives is added to data, which is looked at again (at most
// enterRepeatMax times while only line breaks arrive). Data that holds a
// paste marker is left to the bracketed paste.
func (in *editInput) settle(data []byte, size int) []byte {
	if bytes.Contains(data, pasteStart) {
		return data
	}
	for repeats := 0; ; repeats++ {
		first := bytes.IndexAny(data, "\r\n")
		if first < 0 {
			return data
		}
		if len(bytes.Trim(data[first:], "\r\n")) > 0 {
			in.burst, in.pasting, in.pasteOut = true, true, true
			return data
		}
		if repeats >= enterRepeatMax {
			return data
		}
		more, arrived := in.readWithin(enterQuietTime, size)
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
// (or the read failed) the burst ends with the paste end marker, behind
// what a read held back of a character it cut.
func (in *editInput) fillBurst(size int) {
	in.pasteOut = true
	more, arrived := in.readWithin(burstQuietTime, size)
	if !arrived {
		in.out = append(append([]byte(nil), in.held...), pasteEnd...)
		in.held, in.burst, in.pasting = nil, false, false
		return
	}
	data := append(append([]byte(nil), in.held...), more...)
	in.held = nil
	in.out = in.translate(data, false)
}
