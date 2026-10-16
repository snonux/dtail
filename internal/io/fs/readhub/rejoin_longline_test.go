package readhub

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

// cutLineLength is the MaxLineLength of the rejoin tests across cut lines.
const cutLineLength = 32

// cutLineLengths are the lengths of the burst lines of those tests, cycled:
// each line is cut into two to four pieces, of which the last may be a single
// byte or a whole piece.
var cutLineLengths = []int{33, 40, 64, 65, 100, 128}

// cutBurst returns burstLines lines of cutLineLengths. Every piece of a line
// holds its number, so that each piece is told apart from all others.
func cutBurst(prefix string) []string {
	lines := make([]string, burstLines)
	for i := range lines {
		token := fmt.Sprintf("%s-%05d.", prefix, i)
		n := cutLineLengths[i%len(cutLineLengths)]
		lines[i] = strings.Repeat(token, n/len(token)+1)[:n]
	}
	return lines
}

// lastPieceBefore returns the last piece a follow reader of cutLineLength
// passes on from a line of which only the first partial bytes were written:
// a piece is cut only once more than cutLineLength bytes of the line are
// there, as a line of exactly the limit stays whole. partial must exceed
// cutLineLength.
func lastPieceBefore(text string, partial int) string {
	pieces := (partial - 1) / cutLineLength
	return text[(pieces-1)*cutLineLength : pieces*cutLineLength]
}

// appendPacedSplit appends lines like appendPaced, but writes each batch in
// two parts split in the middle of one of its lines, and waits until fast got
// the pieces of that line before the split, so that the shared reader reads
// the batch with an unfinished line at the end of a read.
func appendPacedSplit(t *testing.T, file *testFile, fast *lastLineProcessor, lines []string) {
	t.Helper()
	const batch = 1500
	for n, start := 0, 0; start < len(lines); n, start = n+1, start+batch {
		end := min(start+batch, len(lines))
		mid := start + (end-start)/2
		// A line long enough for a split after its first piece.
		for len(lines[mid]) < cutLineLength+2 {
			mid++
		}
		split := lines[mid]
		// A split after the first piece and before the line's end.
		partial := cutLineLength + 1 + n%(len(split)-cutLineLength-1)
		head := strings.Join(lines[start:mid], "\n") + "\n" + split[:partial]
		file.appendRaw(head)
		waitWithin(t, 3*waitTimeout, "fast session to get the first part of a batch", func() bool {
			return fast.saw(lastPieceBefore(split, partial))
		})
		file.appendRaw(split[partial:] + "\n" + strings.Join(lines[mid+1:end], "\n") + "\n")
		last := lines[end-1]
		waitWithin(t, 3*waitTimeout, "fast session to get a batch", func() bool {
			return fast.saw(last[(len(last)-1)/cutLineLength*cutLineLength:])
		})
	}
}

// An evicted session that cuts long lines, with bursts of lines written in
// parts split in the middle of a line, gets the same pieces with the same
// numbers from its private reader and after it rejoined as a private follow
// read of the same MaxLineLength, across several eviction and rejoin cycles.
func TestEvictedSessionRejoinsAcrossCutLines(t *testing.T) {
	logger := &capturingLogger{}
	hub := newRejoiningHub(logger)
	// Set before any session joins, so every reader of the hub cuts at it.
	hub.options.MaxLineLength = cutLineLength
	file := newTestFile(t)
	start, err := endOfFile(file.target())
	if err != nil {
		t.Fatal(err)
	}
	private := limitedPrivateFollower(t, file, lcontext.LContext{}, regex.NewNoop(), start, cutLineLength, nil)
	fast := startFastFollower(t, hub, file)
	waitFor(t, "fast session to join", func() bool { return subscriberCount(hub, file.path) == 1 })
	pauser := &pauser{}
	slow := startPausableFollower(t, hub, file, lcontext.LContext{}, regex.NewNoop(), pauser)
	waitFor(t, "slow session to join", func() bool { return sessionSubscribed(hub, file.path, "slow") })

	const cycles = 2
	for cycle := 1; cycle <= cycles; cycle++ {
		pauser.pause()
		appendPacedSplit(t, file, fast, cutBurst(fmt.Sprintf("c%d", cycle)))
		waitWithin(t, 3*waitTimeout, fmt.Sprintf("eviction %d", cycle), func() bool {
			return !sessionSubscribed(hub, file.path, "slow")
		})
		pauser.resume()
		// The session reads the whole burst privately before it rejoins.
		waitWithin(t, 6*waitTimeout, fmt.Sprintf("rejoin %d", cycle), func() bool {
			return sessionSubscribed(hub, file.path, "slow")
		})
		// A long line written in two parts while it is subscribed again.
		file.appendRaw(fmt.Sprintf("tail %d ", cycle) + strings.Repeat("y", 30))
		file.appendRaw(strings.Repeat("z", 40) + "\n")
	}
	file.appendLines("tail end")
	waitWithin(t, 3*waitTimeout, "slow session to get the last line", func() bool { return slow.recorder.hasLine("tail end") })
	waitWithin(t, 3*waitTimeout, "private read to get the last line", func() bool { return private.hasLine("tail end") })
	// Let a wrong extra line, should there be one, arrive too.
	time.Sleep(100 * time.Millisecond)

	assertSameStream(t, slow.recorder, private)
	if n := logger.count("INFO", "evicted a slow subscriber"); n < cycles {
		t.Errorf("logged %d evictions, want at least %d", n, cycles)
	}
	// The paced writes kept the fast session in the shared read throughout.
	if !sessionSubscribed(hub, file.path, "fast") {
		t.Error("the fast session is no longer subscribed to the shared read")
	}
}
