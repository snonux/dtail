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
// the pieces of that line before the split, so that the shared reader ends a
// read with an unfinished line and publishes its first pieces before the rest
// is written. Subscribed sessions get the line in two chunks then. The split
// does not decide where a session is evicted: every cut line is two queue
// items (its long line warning and the chunk it starts), so a full queue
// overflows at a line end long before the split line (see
// TestEvictionInTheMiddleOfACutLine for an eviction there).
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

// A session that cuts long lines, evicted by bursts of cut lines written in
// parts split in the middle of a line, gets the same pieces with the same
// numbers and long line warnings as a private follow read of the same
// MaxLineLength, from the shared reader, its private reader and after it
// rejoined, across several eviction and rejoin cycles. The evictions happen
// at the end of a line (see appendPacedSplit); after a rejoin, a cut line
// written in two parts reaches the session through the shared reader.
func TestEvictedSessionRejoinsAcrossBurstsOfCutLines(t *testing.T) {
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
	// The bursts evict the fast session too, as each cut line is two queue
	// items; it is subscribed again at the end.
	waitFor(t, "fast session to be subscribed again", func() bool {
		return sessionSubscribed(hub, file.path, "fast")
	})
}

// TestEvictionInTheMiddleOfACutLine evicts a session right after the first
// piece of a cut line, deliberately: its queue of two items holds that line's
// long line warning and the chunk of its first piece, which the shared reader
// published at the end of a read that ended in the line, while the session is
// held up in an earlier line. The chunk with the rest of the line does not
// fit. The session's private reader then starts in the middle of the line,
// where the shared reader cut it, and the session must still get the same
// pieces with the same numbers as a private follow read of the same
// MaxLineLength, and one long line warning per cut line, also after it
// rejoined. Every other write waits for the sessions to get it, so that only
// that one eviction happens: the fast session is never evicted.
func TestEvictionInTheMiddleOfACutLine(t *testing.T) {
	logger := &capturingLogger{}
	hub := New(Options{Logger: logger, RetryInterval: 10 * time.Millisecond,
		QueueChunks: 2, MaxLineLength: cutLineLength})
	hub.seams.rejoinMinDelay = 20 * time.Millisecond
	hub.seams.rejoinMaxDelay = 100 * time.Millisecond
	file := newTestFile(t)
	start, err := endOfFile(file.target())
	if err != nil {
		t.Fatal(err)
	}
	private := limitedPrivateFollower(t, file, lcontext.LContext{}, regex.NewNoop(), start, cutLineLength, nil)
	fast := startFastFollower(t, hub, file)
	waitFor(t, "fast session to join", func() bool { return sessionSubscribed(hub, file.path, "fast") })
	pauser := &pauser{}
	messages := make(chan string, 16)
	slow := startPausableFollowerWithMessages(t, hub, file, lcontext.LContext{}, regex.NewNoop(), pauser, messages)
	waitFor(t, "slow session to join", func() bool { return sessionSubscribed(hub, file.path, "slow") })

	cut := evictAtACut(t, hub, file, fast, pauser)
	if n := logger.count("INFO", "evicted a slow subscriber"); n != 1 {
		t.Fatalf("logged %d evictions, want the slow session's one", n)
	}
	pauser.resume()
	// The eviction landed on the cut: the private reader goes on there.
	waitFor(t, "slow session to follow privately from the cut", func() bool {
		return logger.count("INFO", "Following privately after eviction from the shared read",
			fmt.Sprintf(" offset %d\n", start.offset+cut)) == 1
	})
	rejoinAfterACut(t, hub, file, fast, slow, private)

	// Let a wrong extra line or warning, should there be one, arrive too.
	time.Sleep(100 * time.Millisecond)
	assertSameStream(t, slow.recorder, private)
	if n := logger.count("INFO", "evicted a slow subscriber"); n != 1 {
		t.Errorf("logged %d evictions, want only the slow session's one", n)
	}
	if !sessionSubscribed(hub, file.path, "fast") {
		t.Error("the fast session is not subscribed to the shared read")
	}
	// The three cut lines, the first warned about by the shared reader only.
	if n := len(messages); n != 3 {
		t.Errorf("slow session got %d long line warnings, want 3", n)
	}
}

// evictAtACut holds slow up in a line, has the shared reader publish the
// first piece of a cut line at the end of a read, which fills the queue of
// two, and then the rest of the line, which evicts slow. It returns the
// offset of the cut from the end of the file before it wrote, where slow's
// read got to.
func evictAtACut(t *testing.T, hub *Hub, file *testFile, fast *lastLineProcessor, pauser *pauser) int64 {
	t.Helper()
	held := pauser.pauseAt("pause")
	file.appendRaw("pause\n")
	waitFor(t, "fast session to get the pause line", func() bool { return fast.saw("pause") })
	select {
	case <-held:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the slow session to be held up")
	}
	long := strings.Repeat("A", cutLineLength) + strings.Repeat("B", cutLineLength) +
		strings.Repeat("C", cutLineLength) + "DDDD"
	// A read of the line's first 40 bytes: the warning and the chunk of the
	// first piece, two items, fill slow's queue.
	file.appendRaw(long[:cutLineLength+8])
	waitFor(t, "fast session to get the first piece", func() bool {
		return fast.saw(strings.Repeat("A", cutLineLength))
	})
	// The chunk of the other pieces does not fit.
	file.appendRaw(long[cutLineLength+8:] + "\n")
	waitFor(t, "fast session to get the last piece", func() bool { return fast.saw("DDDD") })
	waitFor(t, "slow session to be evicted", func() bool { return !sessionSubscribed(hub, file.path, "slow") })
	return int64(len("pause\n") + cutLineLength)
}

// rejoinAfterACut writes a cut line the evicted slow session reads privately,
// waits until it rejoined, and writes a cut line in two parts and a last line
// through the shared reader, each once both sessions got the one before.
func rejoinAfterACut(t *testing.T, hub *Hub, file *testFile, fast *lastLineProcessor,
	slow *follower, private *recorder) {

	t.Helper()
	file.appendRaw(strings.Repeat("E", cutLineLength) + strings.Repeat("F", cutLineLength) + "GGGGGG\nafter\n")
	waitFor(t, "both sessions to get the line after", func() bool {
		return fast.saw("after") && slow.recorder.hasLine("after")
	})
	waitWithin(t, 3*waitTimeout, "slow session to rejoin", func() bool {
		return sessionSubscribed(hub, file.path, "slow")
	})
	head := "tail " + strings.Repeat("y", 30)
	file.appendRaw(head)
	waitFor(t, "both sessions to get the first piece", func() bool {
		return fast.saw(head[:cutLineLength]) && slow.recorder.hasLine(head[:cutLineLength])
	})
	file.appendRaw(strings.Repeat("z", 40) + "\ntail end\n")
	waitFor(t, "every reader to get the last line", func() bool {
		return fast.saw("tail end") && slow.recorder.hasLine("tail end") && private.hasLine("tail end")
	})
}
