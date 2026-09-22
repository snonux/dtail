package readhub

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

// pauser holds up a session's processors while paused, like a client that
// stopped reading for a while, as often as a test likes.
type pauser struct {
	mu   sync.Mutex
	gate chan struct{}
}

func (p *pauser) pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate == nil {
		p.gate = make(chan struct{})
	}
}

func (p *pauser) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate != nil {
		close(p.gate)
		p.gate = nil
	}
}

func (p *pauser) wait() {
	p.mu.Lock()
	gate := p.gate
	p.mu.Unlock()
	if gate != nil {
		<-gate
	}
}

// pausedProcessor processes a line only while its pauser is not paused.
type pausedProcessor struct {
	line.Processor
	pauser *pauser
}

func (p pausedProcessor) ProcessLine(buf *bytes.Buffer, lineNum uint64, source string) error {
	p.pauser.wait()
	return p.Processor.ProcessLine(buf, lineNum, source)
}

// SourceRestarted passes the restart on, which the embedded interface hides.
func (p pausedProcessor) SourceRestarted() {
	if restarter, ok := p.Processor.(line.SourceRestarter); ok {
		restarter.SourceRestarted()
	}
}

// startPausableFollower starts a session whose processors wait while pauser
// is paused. The cleanup resumes it, should the test have failed while it
// was paused.
func startPausableFollower(t *testing.T, hub *Hub, file *testFile, ltx lcontext.LContext,
	re regex.Regex, pauser *pauser) *follower {

	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &follower{recorder: &recorder{}, cancel: cancel, done: make(chan error, 1)}
	session := Session{
		Target: file.target(), FilePath: file.path, GlobID: "slow", LContext: ltx, Regex: re,
		NewProcessor: func() line.Processor {
			return pausedProcessor{Processor: f.recorder.newProcessor(), pauser: pauser}
		},
	}
	go func() { f.done <- hub.Follow(ctx, session) }()
	t.Cleanup(func() {
		pauser.resume()
		cancel()
		<-f.done
	})
	return f
}

// newRejoiningHub is newEvictingHub with a short pause between declined
// rejoin attempts.
func newRejoiningHub(logger *capturingLogger) *Hub {
	hub := newEvictingHub(logger)
	hub.seams.rejoinMinDelay = 20 * time.Millisecond
	hub.seams.rejoinMaxDelay = 100 * time.Millisecond
	return hub
}

// streamEvents returns the lines, with their numbers and processors, and the
// restarts a recorder saw.
func streamEvents(r *recorder) []string {
	var events []string
	for _, e := range r.snapshot() {
		switch e.kind {
		case "line":
			events = append(events, fmt.Sprintf("%d:%d:%s", e.id, e.lineNum, e.text))
		case "restart":
			events = append(events, fmt.Sprintf("%d:restart", e.id))
		}
	}
	return events
}

// assertSameStream fails unless the session got what the private read did.
func assertSameStream(t *testing.T, session, private *recorder) {
	t.Helper()
	got, want := streamEvents(session), streamEvents(private)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("session got %d events, a private read %d; first difference %s",
			len(got), len(want), firstDifference(got, want))
	}
}

// evictAndRejoin pauses slow while a burst is appended, until it is evicted
// for the n-th time, then resumes it and waits until it rejoined for the n-th
// time.
func evictAndRejoin(t *testing.T, file *testFile, logger *capturingLogger, fast *lastLineProcessor,
	pauser *pauser, n int, prefix string) {

	t.Helper()
	pauser.pause()
	appendPaced(t, file, fast, burst(prefix))
	waitFor(t, fmt.Sprintf("eviction %d", n), func() bool {
		return logger.count("INFO", "evicted a slow subscriber") >= n
	})
	pauser.resume()
	// The session reads the whole burst privately before it rejoins.
	waitWithin(t, 3*waitTimeout, fmt.Sprintf("rejoin %d", n), func() bool {
		return logger.count("INFO", "rejoined the shared follow read") >= n
	})
}

// An evicted session that caught up privately rejoins the shared reader, and
// gets the same lines, numbers and context as one private read, across
// several eviction and rejoin cycles.
func TestEvictedSessionRejoinsWithTheOutputOfAPrivateRead(t *testing.T) {
	contexts := []lcontext.LContext{{}, {BeforeContext: 2}, {AfterContext: 3}, {BeforeContext: 1, AfterContext: 2}}
	for _, ltx := range contexts {
		t.Run(fmt.Sprintf("%+v", ltx), func(t *testing.T) {
			t.Parallel()
			logger := &capturingLogger{}
			hub := newRejoiningHub(logger)
			file := newTestFile(t)
			re := mustRegex(t, "ERROR|tail")
			start, err := endOfFile(file.target())
			if err != nil {
				t.Fatal(err)
			}
			private := privateFollower(t, file, ltx, re, start)
			fast := startFastFollower(t, hub, file)
			waitFor(t, "fast session to join", func() bool { return subscriberCount(hub, file.path) == 1 })
			pauser := &pauser{}
			slow := startPausableFollower(t, hub, file, ltx, re, pauser)
			waitFor(t, "slow session to join", func() bool { return subscriberCount(hub, file.path) == 2 })

			const cycles = 3
			for cycle := 1; cycle <= cycles; cycle++ {
				evictAndRejoin(t, file, logger, fast, pauser, cycle, fmt.Sprintf("cycle%d", cycle))
				// Lines written while it is subscribed again.
				file.appendLines(fmt.Sprintf("tail %d", cycle), "INFO filler", "INFO filler 2", "INFO filler 3")
			}
			file.appendLines("tail end")
			waitFor(t, "slow session to get the last line", func() bool { return slow.recorder.hasLine("tail end") })
			waitFor(t, "private read to get the last line", func() bool { return private.hasLine("tail end") })
			waitFor(t, "slow session to be subscribed", func() bool {
				return sessionSubscribed(hub, file.path, "slow")
			})
			// Let a wrong extra line, should there be one, arrive too.
			time.Sleep(100 * time.Millisecond)

			assertSameStream(t, slow.recorder, private)
			if n := logger.count("INFO", "rejoined the shared follow read", "subscribers="); n < cycles {
				t.Errorf("logged %d rejoins with the subscriber count, want at least %d", n, cycles)
			}
		})
	}
}

// sessionSubscribed reports whether the session with globID is subscribed to
// the shared read of path.
func sessionSubscribed(hub *Hub, path, globID string) bool {
	e := hub.entryFor(path)
	if e == nil {
		return false
	}
	for _, sub := range e.snapshot() {
		if sub.session.GlobID == globID {
			return true
		}
	}
	return false
}

// The hand-over from the private reader to the shared stream happens while
// lines are appended all the time: no line is lost or delivered twice at the
// boundary.
func TestRejoinUnderConcurrentAppendsLosesAndDuplicatesNoLine(t *testing.T) {
	for round := range 3 {
		t.Run(fmt.Sprintf("round%d", round), func(t *testing.T) {
			t.Parallel()
			logger := &capturingLogger{}
			hub := newRejoiningHub(logger)
			file := newTestFile(t)
			start, err := endOfFile(file.target())
			if err != nil {
				t.Fatal(err)
			}
			private := privateFollower(t, file, lcontext.LContext{}, regex.NewNoop(), start)
			fast := startFastFollower(t, hub, file)
			waitFor(t, "fast session to join", func() bool { return subscriberCount(hub, file.path) == 1 })
			pauser := &pauser{}
			slow := startPausableFollower(t, hub, file, lcontext.LContext{}, regex.NewNoop(), pauser)
			waitFor(t, "slow session to join", func() bool { return subscriberCount(hub, file.path) == 2 })

			pauser.pause()
			appendPaced(t, file, fast, burst("burst"))
			waitFor(t, "eviction", func() bool { return logger.count("INFO", "evicted a slow subscriber") >= 1 })

			// A writer appends small batches all along while the session
			// catches up and rejoins.
			var stop atomic.Bool
			written := make(chan int, 1)
			go func() {
				n := 0
				for !stop.Load() {
					var batch strings.Builder
					for range 20 {
						fmt.Fprintf(&batch, "w %06d\n", n)
						n++
					}
					file.appendRaw(batch.String())
					time.Sleep(time.Millisecond)
				}
				written <- n
			}()
			pauser.resume()
			waitWithin(t, 3*waitTimeout, "rejoin", func() bool { return logger.count("INFO", "rejoined the shared follow read") >= 1 })
			time.Sleep(50 * time.Millisecond)
			stop.Store(true)
			last := fmt.Sprintf("w %06d", <-written-1)
			waitFor(t, "slow session to get the last line", func() bool { return slow.recorder.hasLine(last) })
			waitFor(t, "private read to get the last line", func() bool { return private.hasLine(last) })
			time.Sleep(100 * time.Millisecond)

			assertSameStream(t, slow.recorder, private)
		})
	}
}

// A session whose private read reaches the end of the file while the path
// was rotated, and the shared reader moved on to the new file, stays private:
// it reads the rest of the old file, then the new one from its beginning,
// and only then rejoins, in the new file.
func TestRejoinAfterARotationWhileEvicted(t *testing.T) {
	logger := &capturingLogger{}
	hub := newRejoiningHub(logger)
	file := newTestFile(t)
	re := mustRegex(t, "ERROR|new|tail")
	ltx := lcontext.LContext{BeforeContext: 1, AfterContext: 1}
	start, err := endOfFile(file.target())
	if err != nil {
		t.Fatal(err)
	}
	private := privateFollower(t, file, ltx, re, start)
	fast := startFastFollower(t, hub, file)
	waitFor(t, "fast session to join", func() bool { return subscriberCount(hub, file.path) == 1 })
	pauser := &pauser{}
	slow := startPausableFollower(t, hub, file, ltx, re, pauser)
	waitFor(t, "slow session to join", func() bool { return subscriberCount(hub, file.path) == 2 })

	pauser.pause()
	appendPaced(t, file, fast, burst("old"))
	waitFor(t, "eviction", func() bool { return logger.count("INFO", "evicted a slow subscriber") >= 1 })
	if err := os.Rename(file.path, file.path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.path, []byte("new 1\nINFO new 2\nnew 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fast session to read the new file", func() bool { return fast.saw("new 3") })
	pauser.resume()
	waitWithin(t, 3*waitTimeout, "rejoin", func() bool { return logger.count("INFO", "rejoined the shared follow read") >= 1 })
	file.appendLines("INFO a", "tail 1", "INFO b")
	waitFor(t, "slow session to get the last line", func() bool { return slow.recorder.hasLine("INFO b") })
	waitFor(t, "private read to get the last line", func() bool { return private.hasLine("INFO b") })
	time.Sleep(100 * time.Millisecond)

	assertSameStream(t, slow.recorder, private)
	if n := logger.count("WARN", "rotated"); n != 0 {
		t.Errorf("warned %d times about lines lost to the rotation, none were: %q", n, logger.lines)
	}
}

// The path is rotated right after the session rejoined: it gets the rest of
// the old file and the new file from the shared reader, like a private read.
func TestRotationRightAfterARejoin(t *testing.T) {
	logger := &capturingLogger{}
	hub := newRejoiningHub(logger)
	file := newTestFile(t)
	re := mustRegex(t, "ERROR|new|tail")
	start, err := endOfFile(file.target())
	if err != nil {
		t.Fatal(err)
	}
	private := privateFollower(t, file, lcontext.LContext{}, re, start)
	fast := startFastFollower(t, hub, file)
	waitFor(t, "fast session to join", func() bool { return subscriberCount(hub, file.path) == 1 })
	pauser := &pauser{}
	slow := startPausableFollower(t, hub, file, lcontext.LContext{}, re, pauser)
	waitFor(t, "slow session to join", func() bool { return subscriberCount(hub, file.path) == 2 })

	evictAndRejoin(t, file, logger, fast, pauser, 1, "old")
	file.appendLines("tail old")
	if err := os.Rename(file.path, file.path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.path, []byte("new 1\nINFO new 2\nnew 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "slow session to read the new file", func() bool { return slow.recorder.hasLine("new 3") })
	waitFor(t, "private read to read the new file", func() bool { return private.hasLine("new 3") })
	time.Sleep(100 * time.Millisecond)

	assertSameStream(t, slow.recorder, private)
}

// The file is truncated and rewritten while the session is evicted and has
// not read the old content yet: its private reader finds the truncation,
// starts over like a private read, and the session rejoins in the rewritten
// content, getting each of its lines once. The rest of the old content is
// gone for it, as for any reader that had not read it before the truncation.
func TestRejoinAfterATruncationWhileEvicted(t *testing.T) {
	logger := &capturingLogger{}
	hub := newRejoiningHub(logger)
	file := newTestFile(t)
	start, err := endOfFile(file.target())
	if err != nil {
		t.Fatal(err)
	}
	private := privateFollower(t, file, lcontext.LContext{}, regex.NewNoop(), start)
	fast := startFastFollower(t, hub, file)
	waitFor(t, "fast session to join", func() bool { return subscriberCount(hub, file.path) == 1 })
	pauser := &pauser{}
	slow := startPausableFollower(t, hub, file, lcontext.LContext{}, regex.NewNoop(), pauser)
	waitFor(t, "slow session to join", func() bool { return subscriberCount(hub, file.path) == 2 })

	pauser.pause()
	appendPaced(t, file, fast, burst("old"))
	waitFor(t, "eviction", func() bool { return logger.count("INFO", "evicted a slow subscriber") >= 1 })
	if err := os.WriteFile(file.path, []byte("rewritten 1\nrewritten 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fast session to read the rewritten file", func() bool { return fast.saw("rewritten 2") })
	waitFor(t, "private read to read the rewritten file", func() bool { return private.hasLine("rewritten 2") })
	pauser.resume()
	waitWithin(t, 3*waitTimeout, "rejoin", func() bool { return logger.count("INFO", "rejoined the shared follow read") >= 1 })
	file.appendLines("after 1", "after 2")
	waitFor(t, "slow session to get the last line", func() bool { return slow.recorder.hasLine("after 2") })
	waitFor(t, "private read to get the last line", func() bool { return private.hasLine("after 2") })
	time.Sleep(100 * time.Millisecond)

	// After the restart, the session got what the private read got after
	// it, with numbers carrying on from the old content.
	got, want := afterRestart(t, slow.recorder), afterRestart(t, private)
	if wantText := []string{"rewritten 1", "rewritten 2", "after 1", "after 2"}; !reflect.DeepEqual(got, wantText) ||
		!reflect.DeepEqual(want, wantText) {
		t.Errorf("after the restart the session got %q, the private read %q, want %q", got, want, wantText)
	}
	nums := slow.recorder.lineNums()
	for i := 1; i < len(nums); i++ {
		if nums[i] != nums[i-1]+1 {
			t.Fatalf("line numbers are not consecutive at %d: %d then %d", i, nums[i-1], nums[i])
		}
	}
}

// afterRestart returns the lines r got after its only restart.
func afterRestart(t *testing.T, r *recorder) []string {
	t.Helper()
	var lines []string
	restarts := 0
	for _, e := range r.snapshot() {
		switch {
		case e.kind == "restart":
			restarts++
		case e.kind == "line" && restarts > 0:
			lines = append(lines, e.text)
		}
	}
	if restarts != 1 {
		t.Errorf("%d restarts, want 1", restarts)
	}
	return lines
}

// entryPublishedAt returns an entry whose reader has file open and published
// its lines up to published.
func entryPublishedAt(file os.FileInfo, published position) *entry {
	return &entry{openedFile: file, published: published, logger: (&capturingLogger{})}
}

func TestEntryFeedsPast(t *testing.T) {
	file := newTestFile(t)
	info, err := os.Stat(file.path)
	if err != nil {
		t.Fatal(err)
	}
	other := newTestFile(t)
	otherInfo, err := os.Stat(other.path)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		opened    os.FileInfo
		published position
		at        position
		want      bool
	}{
		{"reader behind the session", info, position{offset: 10, file: info}, position{offset: 20, file: info}, true},
		{"reader just there", info, position{offset: 20, file: info}, position{offset: 20, file: info}, true},
		{"reader at the start of the file", info, position{offset: 0, file: info}, position{offset: 20, file: info}, true},
		{"reader ahead of the session", info, position{offset: 21, file: info}, position{offset: 20, file: info}, false},
		{"reader in another file", otherInfo, position{offset: 0, file: otherInfo}, position{offset: 20, file: info}, false},
		{"published position of another file", info, position{offset: 0, file: otherInfo}, position{offset: 20, file: info}, false},
		{"between two reads", nil, unknownPosition(), position{offset: 20, file: info}, false},
		{"published position unknown", info, unknownPosition(), position{offset: 20, file: info}, false},
		{"session position unknown", info, position{offset: 0, file: info}, unknownPosition(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := entryPublishedAt(tt.opened, tt.published)
			if got := e.feedsPast(tt.at); got != tt.want {
				t.Errorf("feedsPast() = %v, want %v", got, tt.want)
			}
			sub := newSubscriber(Session{}, 1)
			// Evicted before: its descriptor was taken.
			sub.held.take()
			held, err := os.Open(file.path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = held.Close() }()
			if got := e.rejoin(sub, tt.at, held); got != tt.want {
				t.Errorf("rejoin() = %v, want %v", got, tt.want)
			}
			if subscribed := len(e.snapshot()) == 1; subscribed != tt.want {
				t.Errorf("subscribed = %v, want %v", subscribed, tt.want)
			}
			if holds := sub.held.take() == held; holds != tt.want {
				t.Errorf("session holds the descriptor = %v, want %v", holds, tt.want)
			}
		})
	}
}

// The published position follows what the entry publishes.
// A declined rejoin closes the descriptor it opened for the session.
func TestDeclinedRejoinClosesItsDescriptor(t *testing.T) {
	hub := newTestHub()
	opened := recordOpenedFiles(hub)
	file := newTestFile(t)
	info, err := os.Stat(file.path)
	if err != nil {
		t.Fatal(err)
	}
	session := Session{Target: file.target(), FilePath: file.path, NewProcessor: (&recorder{}).newProcessor}
	// The shared reader is ahead of the session.
	e := entryPublishedAt(info, position{offset: info.Size(), file: info})
	hub.entries[sessionKey(session)] = e
	sub := newSubscriber(session, 1)

	if hub.rejoin(sub, position{offset: 1, file: info}) {
		t.Fatal("rejoined a shared reader that is ahead of the session")
	}
	if len(e.snapshot()) != 0 {
		t.Error("the declined session was subscribed")
	}
	if len(opened.files) != 1 {
		t.Fatalf("opened %d descriptors, want 1", len(opened.files))
	}
	if n := opened.open(); n != 0 {
		t.Errorf("%d descriptors are still open after the declined rejoin", n)
	}
}

func TestEntryTracksThePublishedPosition(t *testing.T) {
	file := newTestFile(t)
	info, err := os.Stat(file.path)
	if err != nil {
		t.Fatal(err)
	}
	e := &entry{logger: &capturingLogger{}, readPos: unknownPosition(), published: unknownPosition()}
	e.readUpTo(position{offset: 5, file: info})
	if e.published != (position{offset: 5, file: info}) {
		t.Errorf("after the open published = %+v, want the start offset 5", e.published)
	}
	e.readUpTo(position{offset: 50, file: info})
	if e.published.offset != 5 {
		t.Errorf("reading on moved the published position to %d", e.published.offset)
	}
	e.publish(item{kind: chunkItem, chunk: &chunk{data: []byte("ab"), ends: []int{1, 2},
		offsets: []int64{10, 12}, file: info}})
	if e.published != (position{offset: 12, file: info}) {
		t.Errorf("after a chunk published = %+v, want its last line end 12", e.published)
	}
	e.publish(item{kind: restartItem})
	if e.published != (position{offset: 0, file: info}) {
		t.Errorf("after a restart published = %+v, want the start of the file", e.published)
	}
	e.publish(item{kind: reopenItem})
	if e.published.known() {
		t.Errorf("after a reopen published = %+v, want an unknown position", e.published)
	}
	e.publish(item{kind: failedItem, err: errProcessor})
	if !e.isClosed() {
		t.Error("a failed entry takes new subscribers")
	}
}

func TestRejoinPolicyBacksOff(t *testing.T) {
	now := time.Unix(0, 0)
	policy := newRejoinPolicy(hubSeams{now: func() time.Time { return now },
		rejoinMinDelay: time.Second, rejoinMaxDelay: 4 * time.Second})
	advance := func(d time.Duration) { now = now.Add(d) }
	steps := []struct {
		name string
		do   func()
		due  bool
	}{
		{"first attempt", func() {}, true},
		{"declined", policy.declined, false},
		{"after the first pause", func() { advance(time.Second) }, true},
		{"declined again", policy.declined, false},
		{"pause doubled", func() { advance(time.Second) }, false},
		{"after the doubled pause", func() { advance(time.Second) }, true},
		{"declined a third time", policy.declined, false},
		{"pause bounded", func() { advance(4 * time.Second) }, true},
		{"rejoined", policy.rejoined, true},
		{"evicted soon after", func() { advance(time.Second); policy.evicted() }, false},
		{"after the pause", func() { advance(4 * time.Second) }, true},
		{"rejoined again", policy.rejoined, true},
		{"evicted after a long stay", func() { advance(5 * time.Second); policy.evicted() }, true},
	}
	for _, step := range steps {
		step.do()
		if got := policy.due(); got != step.due {
			t.Fatalf("%s: due() = %v, want %v", step.name, got, step.due)
		}
	}
}

// A session evicted again soon after it rejoined waits before it rejoins
// once more, instead of alternating between the shared reader and a private
// one; it reads privately meanwhile.
func TestSessionEvictedSoonAfterARejoinBacksOff(t *testing.T) {
	logger := &capturingLogger{}
	hub := newEvictingHub(logger)
	var clock atomic.Int64
	hub.seams.now = func() time.Time { return time.Unix(0, clock.Load()) }
	hub.seams.rejoinMinDelay = time.Hour
	hub.seams.rejoinMaxDelay = 2 * time.Hour
	file := newTestFile(t)
	fast := startFastFollower(t, hub, file)
	waitFor(t, "fast session to join", func() bool { return subscriberCount(hub, file.path) == 1 })
	pauser := &pauser{}
	slow := startPausableFollower(t, hub, file, lcontext.LContext{}, regex.NewNoop(), pauser)
	waitFor(t, "slow session to join", func() bool { return subscriberCount(hub, file.path) == 2 })

	// The first eviction: the session rejoins as soon as it caught up.
	evictAndRejoin(t, file, logger, fast, pauser, 1, "first")
	// Evicted again right away: it catches up privately, but does not
	// rejoin before the pause passed.
	pauser.pause()
	appendPaced(t, file, fast, burst("second"))
	waitFor(t, "second eviction", func() bool { return logger.count("INFO", "evicted a slow subscriber") >= 2 })
	pauser.resume()
	file.appendLines("private")
	waitFor(t, "slow session to catch up privately", func() bool { return slow.recorder.hasLine("private") })
	time.Sleep(300 * time.Millisecond)
	if n := logger.count("INFO", "rejoined the shared follow read"); n != 1 {
		t.Fatalf("rejoined %d times before the pause passed, want once", n)
	}
	clock.Add(int64(time.Hour))
	waitFor(t, "rejoin after the pause", func() bool {
		return logger.count("INFO", "rejoined the shared follow read") == 2
	})
}

// openDescriptors counts the process's open descriptors, or returns -1 where
// /proc is not available.
func openDescriptors() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// Repeated evictions and rejoins leak neither goroutines nor descriptors once
// every session left.
func TestRejoinCyclesLeakNothing(t *testing.T) {
	baseFDs, baseGoroutines := openDescriptors(), runtime.NumGoroutine()
	func() {
		logger := &capturingLogger{}
		hub := newRejoiningHub(logger)
		file := newTestFile(t)
		fastCtx, fastCancel := context.WithCancel(context.Background())
		fast := &lastLineProcessor{}
		fastDone := make(chan error, 1)
		go func() {
			fastDone <- hub.Follow(fastCtx, Session{Target: file.target(), FilePath: file.path, GlobID: "fast",
				Regex: regex.NewNoop(), NewProcessor: func() line.Processor { return fast }})
		}()
		waitFor(t, "fast session to join", func() bool { return subscriberCount(hub, file.path) == 1 })
		pauser := &pauser{}
		slow := startPausableFollower(t, hub, file, lcontext.LContext{}, regex.NewNoop(), pauser)
		waitFor(t, "slow session to join", func() bool { return subscriberCount(hub, file.path) == 2 })
		for cycle := 1; cycle <= 3; cycle++ {
			evictAndRejoin(t, file, logger, fast, pauser, cycle, fmt.Sprintf("cycle%d", cycle))
		}
		if err := slow.stop(t); err != nil {
			t.Errorf("slow Follow() = %v", err)
		}
		fastCancel()
		if err := <-fastDone; err != nil {
			t.Errorf("fast Follow() = %v", err)
		}
		waitFor(t, "every shared read to stop", func() bool { return hub.entryFor(file.path) == nil })
	}()

	waitFor(t, "goroutines to end", func() bool { return runtime.NumGoroutine() <= baseGoroutines })
	if baseFDs >= 0 {
		waitFor(t, "descriptors to be closed", func() bool { return openDescriptors() <= baseFDs })
	}
}
