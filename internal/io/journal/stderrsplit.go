//go:build linux

package journal

import (
	"bytes"
	"fmt"
)

// stderrTruncatedNote ends the server message of a journalctl stderr line
// that was cut at maxStderrTokenSize.
var stderrTruncatedNote = fmt.Sprintf(" [truncated: stderr line longer than %d bytes]", maxStderrTokenSize)

// stderrLineSplitter splits journalctl's stderr into lines for bufio.Scanner
// like bufio.ScanLines (without the "\n" and a "\r" before it), except that a
// line longer than limit bytes does not end the scan with bufio.ErrTooLong:
// its first limit bytes are one token, marked truncated, and the rest of the
// line up to its newline is drained and dropped. So one oversized diagnostic
// line can neither cancel the journalctl run nor block the stderr pipe.
type stderrLineSplitter struct {
	limit int
	// discarding drops the rest of a truncated line until its newline.
	discarding bool
	// truncated reports that the last token is the start of a longer line.
	truncated bool
}

// split is the bufio.SplitFunc of the splitter. The scanner's buffer must
// hold at least limit bytes and be capped by limit (scanner.Buffer), so that
// the scanner calls split with limit or more bytes before it gives up on a
// line without newline.
func (s *stderrLineSplitter) split(data []byte, atEOF bool) (int, []byte, error) {
	s.truncated = false
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		if s.discarding {
			s.discarding = false
			return i + 1, nil, nil
		}
		if i > s.limit {
			// A whole line above the limit can reach split at once when the
			// buffer is larger than the limit.
			s.truncated = true
			return i + 1, data[:s.limit], nil
		}
		return i + 1, dropCR(data[:i]), nil
	}
	if s.discarding {
		return len(data), nil, nil
	}
	if len(data) >= s.limit {
		s.discarding = true
		s.truncated = true
		return len(data), data[:s.limit], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), dropCR(data), nil
	}
	return 0, nil, nil
}

// dropCR drops a terminal "\r", as bufio.ScanLines does.
func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[:len(data)-1]
	}
	return data
}

// stderrMessage formats one stderr line as a server message.
func stderrMessage(line []byte, truncated bool) string {
	if truncated {
		return fmt.Sprintf("journalctl stderr: %s%s\n", line, stderrTruncatedNote)
	}
	return fmt.Sprintf("journalctl stderr: %s\n", line)
}
