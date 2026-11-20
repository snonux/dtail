package clients

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
)

// editorDefaultWidth is the width golang.org/x/term's Terminal assumes
// until it learns the window size (SetSize).
const editorDefaultWidth = 80

// pathListingMissingHint is shown when a Tab gave up waiting for a path
// listing: the request goes on, and a later Tab uses its answer.
const pathListingMissingHint = "(no listing of %s from the server within %s: press Tab again to use it once it arrives)"

// tableProbeMissingHint is shown when a Tab completed a query from the
// cache while the table probe of its from table had not answered yet.
const tableProbeMissingHint = "(the servers have not answered the probe of table %s yet: " +
	"press Tab again to use it once it arrives)"

// holdFullHint is shown instead of the two hints above when the wait had
// no chance: the held output is full (outputHoldMaxBytes), so the writers
// block again and no answer of a server is read until the output resumes;
// pressing Tab again would only wait again.
const holdFullHint = "(no answer from the server: the held output is full; let the output run " +
	"(enter the line, or an empty one), then press Tab again)"

// missingAnswerHint is the hint of a Tab whose wait for a server's answer
// ran out: missing, or holdFullHint when the pause's hold is full.
func missingAnswerHint(pause *interactivePause, missing string) string {
	if pause != nil && pause.hold.isFull() {
		return holdFullHint
	}
	return missing
}

// completeEditorLine completes the line being edited at a Tab press: a
// --files value from the path listings (completeFilesValue, waiting for a
// listing that is not cached, awaitPathListing), anything else with
// completeInteractive and, for a query naming a from table,
// completeWithTableProbe.
func (c *baseClient) completeEditorLine(pause *interactivePause, line string, pos int) completion {

	args, _ := c.snapshotConnectionState()
	result, ok := completeFilesValue(line, pos, args, c.prober.pathListingSource())
	if !ok {
		// A method value on a nil prober is fine: catalogSnapshot accepts nil.
		result = completeInteractive(line, pos, args, c.prober.catalogSnapshot)
		if result.probeTable != "" {
			result = c.completeWithTableProbe(pause, line, pos, result)
		}
		return result
	}
	if result.fetch == "" {
		return result
	}
	return c.awaitPathListing(pause, line, pos, result)
}

// completeWithTableProbe has the prober ask for a table probe of the from
// table of a query completion (requestTable; nothing is sent while a fresh
// answer is cached or a failed probe of it is recent) and, only for the
// Tab that sent the request, waits for it like a path listing, at most the
// prober's listWait with the paused output held (waitForListing), then
// completes the line again. A later Tab while the probe is still on its way
// does not wait again: it completes from the cache as result does, with
// tableProbeMissingHint, as does the Tab whose wait ran out (holdFullHint
// when the held output is full).
func (c *baseClient) completeWithTableProbe(pause *interactivePause,
	line string, pos int, result completion) completion {

	done, started := c.prober.requestTable(result.probeTable)
	if done == nil {
		return result
	}
	if started && waitForListing(pause, done, c.prober.listWait) {
		args, _ := c.snapshotConnectionState()
		return completeInteractive(line, pos, args, c.prober.catalogSnapshot)
	}
	result.hint = joinLines(result.hint, missingAnswerHint(pause,
		fmt.Sprintf(tableProbeMissingHint, displayText(result.probeTable))))
	return result
}

// awaitPathListing sends the listing request result asks for
// (interactiveProber.listPaths) and waits for it, at most the prober's
// listWait in total, then completes again. A truncated listing can ask for
// a narrower one, which is waited for within the same time. When no
// listing arrived in time (or the server could not answer, e.g. because
// another probe of the session still ran there), the candidates found so
// far are kept and a hint says so; the request goes on, and the next Tab
// uses a late answer from the cache.
func (c *baseClient) awaitPathListing(pause *interactivePause,
	line string, pos int, result completion) completion {

	wait := pathListingWait
	if c.prober != nil {
		wait = c.prober.listWait
	}
	deadline := time.Now().Add(wait)
	for result.fetch != "" {
		done := c.prober.listPaths(result.fetch)
		remaining := time.Until(deadline)
		if done == nil || remaining <= 0 || !waitForListing(pause, done, remaining) {
			break
		}
		args, _ := c.snapshotConnectionState()
		next, _ := completeFilesValue(line, pos, args, c.prober.pathListingSource())
		answered := next.fetch != result.fetch
		result = next
		if !answered {
			// The request ended without a listing to cache.
			break
		}
	}
	if result.fetch != "" {
		dir, _ := listingDirKey(result.fetch)
		result.hint = joinLines(result.hint, missingAnswerHint(pause, fmt.Sprintf(pathListingMissingHint, dir, wait)))
		result.fetch = ""
	}
	return result
}

// waitForListing waits until done (a path listing or table probe request,
// listPaths or requestTable) is closed or timeout passed and reports
// whether it was closed. The paused output is held meanwhile
// (interactivePause.holdOutput): with a remote server the connection handler
// reads the answer only while it can write the output, and in serverless
// mode the in-process server handles the request only while it can write
// its log lines, which go to the paused client output as well (so the
// serverless answer does not arrive while the writers block either,
// although its payload bypasses the handler, see
// TestProbeAnswerWhileOutputPaused). So the writers go on for the wait, but
// what they write is kept and printed only when the output resumes:
// nothing reaches the terminal, and the line being edited stays on the
// screen as it is. The output is always paused here: the Tab that led here
// paused it for typing unless it was paused already (editorKey calls
// startTyping before it completes), so there is no path for running
// output; holdOutput would simply run the wait if it were not.
func waitForListing(pause *interactivePause, done <-chan struct{}, timeout time.Duration) bool {
	var arrived bool
	pause.holdOutput(func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-done:
			arrived = true
		case <-timer.C:
		}
	})
	return arrived
}

// redrawLine has the Terminal draw its prompt and line again, after the
// external editor had the screen (editExternally). Its Write clears the rows
// it believes the line takes and draws prompt and line. x/term releases its
// lock around the key callback, so Write is safe here.
func (e *interactiveEditor) redrawLine() {
	if e != nil && e.terminal != nil {
		_, _ = e.terminal.Write(nil)
	}
}

// joinLines joins the non-empty lines with line breaks.
func joinLines(lines ...string) string {
	var kept []string
	for _, line := range lines {
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// editorScreen is the control TTY next to the Terminal: what the editor
// writes there itself (the bracketed paste and cursor shape sequences) and
// the terminal width the Terminal was last told (setWidth), which the
// candidate listings wrap at. A nil screen, or one without a writer, writes
// nothing.
type editorScreen struct {
	out   io.Writer
	width atomic.Int64
}

func newEditorScreen(out io.Writer) *editorScreen {
	s := &editorScreen{out: out}
	s.width.Store(editorDefaultWidth)
	return s
}

// setWidth records the terminal width the Terminal was told (SetSize).
func (s *editorScreen) setWidth(width int) {
	if s != nil && width > 0 {
		s.width.Store(int64(width))
	}
}

// columns returns the terminal width the Terminal was last told (setWidth),
// editorDefaultWidth for a nil screen.
func (s *editorScreen) columns() int {
	if s == nil {
		return editorDefaultWidth
	}
	return int(s.width.Load())
}
