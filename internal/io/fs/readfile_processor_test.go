package fs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

type captureProcessor struct {
	lines      []string
	lineNums   []uint64
	errAtLine  int
	processErr error
	flushErr   error
}

func (p *captureProcessor) ProcessLine(lineContent *bytes.Buffer, lineNum uint64, _ string) error {
	p.lines = append(p.lines, lineContent.String())
	p.lineNums = append(p.lineNums, lineNum)
	pool.RecycleBytesBuffer(lineContent)

	if p.errAtLine > 0 && len(p.lines) == p.errAtLine {
		return p.processErr
	}
	return nil
}

func (p *captureProcessor) Flush() error {
	return p.flushErr
}

func (p *captureProcessor) Close() error {
	return nil
}

func TestStartWithProcessorOptimizedReadsAllLines(t *testing.T) {
	filePath := writeProcessorTestFile(t, "alpha\nbeta\n")
	re := regex.NewNoop()

	cat := NewCatFile(filePath, "glob-id", make(chan string, 1), defaultMaxLineLength)
	processor := &captureProcessor{}

	if err := cat.readFile.StartWithProcessorOptimized(
		context.Background(),
		lcontext.LContext{},
		processor,
		re,
	); err != nil {
		t.Fatalf("optimized reader start failed: %v", err)
	}

	want := []string{"alpha\n", "beta\n"}
	if !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("unexpected processed lines: got=%v want=%v", processor.lines, want)
	}
}

// TestReadWithProcessorOptimizedDetectsTruncation proves that after the
// per-line time.Since truncate gate was removed (task 2t0), the non-follow
// read loop still detects truncation: when the periodicTruncateCheck goroutine
// signals on the truncate channel, the loop re-stats the file and returns the
// truncation error. The reader (line source) is decoupled from the fd (stat
// source) so the scenario is deterministic without relying on the 3s cadence:
// the file on disk is shorter than the fd's current read position, exactly the
// state truncated() flags. A signal is pre-loaded on the truncate channel so
// the very first loop iteration performs the check.
func TestReadWithProcessorOptimizedDetectsTruncation(t *testing.T) {
	resetCommonLogger(t)

	// The on-disk file is intentionally tiny; the fd is then seeked well past
	// its end to emulate having read a file that shrank underneath us.
	filePath := writeProcessorTestFile(t, "short")

	fd, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	defer fd.Close()
	if _, err := fd.Seek(4096, 0); err != nil {
		t.Fatalf("seek fd past end: %v", err)
	}

	// The scanner reads its lines from an independent in-memory reader so the
	// loop actually iterates and reaches the truncate check.
	reader := bufio.NewReader(strings.NewReader("l1\nl2\nl3\nl4\nl5\n"))

	// Pre-load one truncate signal (buffered) so the first iteration checks.
	truncate := make(chan struct{}, 1)
	truncate <- struct{}{}

	rf := readFile{
		filePath:      filePath,
		globID:        "glob-id",
		maxLineLength: defaultMaxLineLength,
	}

	err = rf.readWithProcessorOptimized(
		context.Background(),
		fd,
		reader,
		truncate,
		lcontext.LContext{},
		&captureProcessor{},
		regex.NewNoop(),
	)
	if err == nil {
		t.Fatal("expected truncation to be detected, got nil error")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncation error, got: %v", err)
	}
}

func TestProcessorVariantsReturnOpenError(t *testing.T) {
	re := regex.NewNoop()
	missingFile := filepath.Join(t.TempDir(), "missing.log")

	tests := []struct {
		name  string
		start func(*readFile, context.Context, lcontext.LContext, *captureProcessor, regex.Regex) error
	}{
		{
			name: "standard",
			start: func(rf *readFile, ctx context.Context, ltx lcontext.LContext, p *captureProcessor, re regex.Regex) error {
				return rf.StartWithProcessor(ctx, ltx, p, re)
			},
		},
		{
			name: "optimized",
			start: func(rf *readFile, ctx context.Context, ltx lcontext.LContext, p *captureProcessor, re regex.Regex) error {
				return rf.StartWithProcessorOptimized(ctx, ltx, p, re)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := NewCatFile(missingFile, "glob-id", make(chan string, 1), defaultMaxLineLength)
			err := tt.start(&cat.readFile, context.Background(), lcontext.LContext{}, &captureProcessor{}, re)
			if err == nil {
				t.Fatalf("expected error for missing file")
			}
		})
	}
}

func TestStartWithProcessorOptimizedPropagatesProcessError(t *testing.T) {
	filePath := writeProcessorTestFile(t, "alpha\nbeta\n")
	re := regex.NewNoop()
	expectedErr := errors.New("processor failure")

	cat := NewCatFile(filePath, "glob-id", make(chan string, 1), defaultMaxLineLength)
	processor := &captureProcessor{
		errAtLine:  1,
		processErr: expectedErr,
	}

	err := cat.readFile.StartWithProcessorOptimized(
		context.Background(),
		lcontext.LContext{},
		processor,
		re,
	)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected process error %v, got %v", expectedErr, err)
	}
}

func TestStartWithProcessorOptimizedUsesInjectedMaxLineLength(t *testing.T) {
	resetCommonLogger(t)

	filePath := writeProcessorTestFile(t, "abcdef\n")
	re := regex.NewNoop()

	cat := NewCatFile(filePath, "glob-id", make(chan string, 1), 3)
	processor := &captureProcessor{}

	if err := cat.readFile.StartWithProcessorOptimized(
		context.Background(),
		lcontext.LContext{},
		processor,
		re,
	); err != nil {
		t.Fatalf("optimized reader start failed: %v", err)
	}

	want := []string{"abc", "def\n"}
	if !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("unexpected processed lines: got=%v want=%v", processor.lines, want)
	}
}

func TestStartWithProcessorOptimizedWaitsOnLiveLongLineWarningUntilCanceled(t *testing.T) {
	resetCommonLogger(t)

	filePath := writeProcessorTestFile(t, strings.Repeat("a", 8))
	re := regex.NewNoop()

	cat := NewCatFile(filePath, "glob-id", make(chan string), 1)
	processor := &captureProcessor{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- cat.readFile.StartWithProcessorOptimized(
			ctx,
			lcontext.LContext{},
			processor,
			re,
		)
	}()

	select {
	case err := <-done:
		t.Fatalf("optimized reader returned before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled optimized reader to stop with nil or context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("optimized reader did not return after cancellation")
	}
}

// TestStartWithProcessorExitsWhenContextCanceledDuringLongLineWarning proves the
// byte-by-byte processor reader (StartWithProcessor) returns cleanly when the
// context is canceled while a long-line warning would otherwise block. The
// optimized reader has equivalent coverage in
// TestStartWithProcessorOptimizedWaitsOnLiveLongLineWarningUntilCanceled. The
// historic channel-based Start reader was removed in task iv0, so only the
// processor variant remains here.
func TestStartWithProcessorExitsWhenContextCanceledDuringLongLineWarning(t *testing.T) {
	resetCommonLogger(t)

	filePath := writeProcessorTestFile(t, strings.Repeat("a", 8))
	re := regex.NewNoop()

	cat := NewCatFile(filePath, "glob-id", make(chan string), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- cat.readFile.StartWithProcessor(ctx, lcontext.LContext{}, &captureProcessor{}, re)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected canceled start to exit cleanly, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("start did not return after context cancellation")
	}
}

func TestTailWithProcessorOptimizedExitsWhenContextCanceledDuringLongLineWarning(t *testing.T) {
	resetCommonLogger(t)

	filePath := writeProcessorTestFile(t, strings.Repeat("a", 8))
	re := regex.NewNoop()

	rf := readFile{
		filePath:       filePath,
		globID:         "glob-id",
		serverMessages: make(chan string),
		retry:          true,
		canSkipLines:   true,
		seekEOF:        false,
		maxLineLength:  1,
	}

	reader, fd, decompressor, err := rf.makeReader()
	if fd != nil {
		defer fd.Close()
	}
	if decompressor != nil {
		defer func() {
			if closeErr := decompressor.Close(); closeErr != nil {
				t.Fatalf("unable to close decompressor: %v", closeErr)
			}
		}()
	}
	if err != nil {
		t.Fatalf("make reader: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- rf.tailWithProcessorOptimized(
			ctx,
			fd,
			reader,
			make(chan struct{}),
			lcontext.LContext{},
			&captureProcessor{},
			re,
		)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected canceled optimized tail to exit cleanly, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("optimized tail did not return after context cancellation")
	}
}

// TestReadWithProcessorNoDoubleRecycle verifies that readWithProcessor does not
// Put the same *bytes.Buffer back into the pool twice. The bug: a stale
// `defer pool.RecycleBytesBuffer(message)` captured the initial buffer pointer
// at defer-registration time; after that buffer was handed off downstream (and
// recycled there) and `message` was reassigned on continueReading, the deferred
// call recycled the already-recycled original buffer. A trailing partial line
// (no final newline) makes the bug deterministic because handleReadErrorProcessor
// also hands the current buffer to ProcessFilteredLine (which recycles it).
func TestReadWithProcessorNoDoubleRecycle(t *testing.T) {
	resetCommonLogger(t)
	drainBytesBufferPool()

	filePath := writeProcessorTestFile(t, "alpha\nbeta")
	re := regex.NewNoop()

	cat := NewCatFile(filePath, "glob-id", make(chan string, 1), defaultMaxLineLength)
	processor := &captureProcessor{}

	if err := cat.readFile.StartWithProcessor(
		context.Background(),
		lcontext.LContext{},
		processor,
		re,
	); err != nil {
		t.Fatalf("reader start failed: %v", err)
	}

	want := []string{"alpha\n", "beta"}
	if !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("unexpected processed lines: got=%v want=%v", processor.lines, want)
	}

	seen := make(map[*bytes.Buffer]int)
	for i := 0; i < 512; i++ {
		b := pool.BytesBuffer.Get().(*bytes.Buffer)
		seen[b]++
		if seen[b] > 1 {
			t.Fatalf("buffer %p observed in pool more than once: "+
				"double-recycle detected (Put twice into sync.Pool)", b)
		}
	}
}

// drainBytesBufferPool empties the global buffer pool of any previously-Put
// entries so that pool inspection in a subsequent test is not polluted by
// artifacts from earlier test runs.
func drainBytesBufferPool() {
	for i := 0; i < 1024; i++ {
		_ = pool.BytesBuffer.Get()
	}
}

// TestReadWithProcessorOptimizedFastPathByteIdentical proves that the no-context
// zero-copy fast path (match on scanner.Bytes() before copying) yields exactly
// the same emitted lines as the previous copy-every-line behavior, across grep
// hit rates, inverted matching, zero matches, and the cat noop (match-all) case.
func TestReadWithProcessorOptimizedFastPathByteIdentical(t *testing.T) {
	const content = "apple\nbanana\napricot\ncherry\navocado\n"

	mustRegex := func(pattern string, flag regex.Flag) regex.Regex {
		re, err := regex.New(pattern, flag)
		if err != nil {
			t.Fatalf("build regex %q: %v", pattern, err)
		}
		return re
	}

	// wantNums, when non-nil, pins the exact lineNum argument passed to
	// ProcessLine for each emitted line. Because f.updatePosition() runs for
	// every scanned line (matching or not) before the filter, non-matching lines
	// still advance the counter, so a match after N non-matches must report
	// lineNum N+1 (1-based) - never restarting at 1. This locks in that the
	// zero-copy fast path counts lines identically to the old copy-every-line
	// path. (apple=1, banana=2, apricot=3, cherry=4, avocado=5.)
	tests := []struct {
		name     string
		re       regex.Regex
		want     []string
		wantNums []uint64
	}{
		{
			name:     "low hit default",
			re:       mustRegex("ap", regex.Default),
			want:     []string{"apple\n", "apricot\n"},
			wantNums: []uint64{1, 3},
		},
		{
			name: "high hit default",
			re:   mustRegex("a", regex.Default),
			want: []string{"apple\n", "banana\n", "apricot\n", "avocado\n"},
		},
		{
			name: "zero match",
			re:   mustRegex("zzz", regex.Default),
			want: nil,
		},
		{
			name:     "invert",
			re:       mustRegex("ap", regex.Invert),
			want:     []string{"banana\n", "cherry\n", "avocado\n"},
			wantNums: []uint64{2, 4, 5},
		},
		{
			name: "noop matches all (cat)",
			re:   regex.NewNoop(),
			want: []string{"apple\n", "banana\n", "apricot\n", "cherry\n", "avocado\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := writeProcessorTestFile(t, content)
			cat := NewCatFile(filePath, "glob-id", make(chan string, 1), defaultMaxLineLength)
			processor := &captureProcessor{}

			if err := cat.readFile.StartWithProcessorOptimized(
				context.Background(),
				lcontext.LContext{},
				processor,
				tt.re,
			); err != nil {
				t.Fatalf("optimized reader start failed: %v", err)
			}

			if !reflect.DeepEqual(processor.lines, tt.want) {
				t.Fatalf("unexpected processed lines: got=%v want=%v", processor.lines, tt.want)
			}
			if tt.wantNums != nil && !reflect.DeepEqual(processor.lineNums, tt.wantNums) {
				t.Fatalf("unexpected line numbers: got=%v want=%v", processor.lineNums, tt.wantNums)
			}
		})
	}
}

// TestReadWithProcessorOptimizedContextPathUnchanged exercises the local-context
// path (ltx.Has() == true), which must keep buffering every line so before/after
// context lines are still emitted. The fast path must NOT be taken here.
func TestReadWithProcessorOptimizedContextPathUnchanged(t *testing.T) {
	const content = "a\nb\nHIT\nd\ne\n"
	re, err := regex.New("HIT", regex.Default)
	if err != nil {
		t.Fatalf("build regex: %v", err)
	}

	filePath := writeProcessorTestFile(t, content)
	cat := NewCatFile(filePath, "glob-id", make(chan string, 1), defaultMaxLineLength)
	processor := &captureProcessor{}

	// One line of before context and one line of after context around the match.
	ltx := lcontext.LContext{BeforeContext: 1, AfterContext: 1}
	if err := cat.readFile.StartWithProcessorOptimized(
		context.Background(),
		ltx,
		processor,
		re,
	); err != nil {
		t.Fatalf("optimized reader start failed: %v", err)
	}

	want := []string{"b\n", "HIT\n", "d\n"}
	if !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("unexpected context lines: got=%v want=%v", processor.lines, want)
	}
}

// TestProcessFilteredRawZeroAllocOnNonMatch locks in the win: a non-matching line
// on the fast path must not acquire a pooled buffer or copy anything, so it
// allocates nothing. A matching line does allocate (buffer copy + emit).
func TestProcessFilteredRawZeroAllocOnNonMatch(t *testing.T) {
	re, err := regex.New("MATCHME", regex.Default)
	if err != nil {
		t.Fatalf("build regex: %v", err)
	}

	var st stats
	fp := &filteringProcessor{
		processor: &captureProcessor{},
		re:        re,
		ltx:       lcontext.LContext{},
		stats:     &st,
		globID:    "glob-id",
	}

	nonMatch := []byte("this line does not contain the needle\n")
	allocs := testing.AllocsPerRun(100, func() {
		if err := fp.ProcessFilteredRaw(nonMatch); err != nil {
			t.Fatalf("ProcessFilteredRaw returned error: %v", err)
		}
	})
	if allocs != 0 {
		t.Fatalf("expected zero allocations on non-matching fast-path line, got %v", allocs)
	}
}

// TestProcessorMaxCountEarlyStopNoErrorLeak is a regression test for the
// optimized read path leaking the io.EOF early-stop sentinel that
// filteringProcessor.processWithContext returns once a -m/-max (MaxCount) limit
// is reached. The byte-by-byte path (StartWithProcessor) already swallowed that
// sentinel and returned nil; the optimized path (StartWithProcessorOptimized)
// used to surface it as an error, which the server then logged as a spurious
// SERVER|...|ERROR|...|EOF line. Both paths must now return nil AND emit
// byte-identical lines for the same MaxCount, proving the sentinel is handled as
// a clean early stop, not a genuine I/O error.
func TestProcessorMaxCountEarlyStopNoErrorLeak(t *testing.T) {
	const content = "match 1\nother\nmatch 2\nother\nmatch 3\nother\nmatch 4\n"
	re, err := regex.New("match", regex.Default)
	if err != nil {
		t.Fatalf("build regex: %v", err)
	}
	// MaxCount without after-context: processWithContext returns io.EOF as soon
	// as the second match is emitted (the -max 2 early stop).
	ltx := lcontext.LContext{MaxCount: 2}

	run := func(start func(*readFile, context.Context, lcontext.LContext, *captureProcessor, regex.Regex) error) *captureProcessor {
		filePath := writeProcessorTestFile(t, content)
		cat := NewCatFile(filePath, "glob-id", make(chan string, 1), defaultMaxLineLength)
		processor := &captureProcessor{}
		if err := start(&cat.readFile, context.Background(), ltx, processor, re); err != nil {
			// A non-nil return here is exactly the leaked sentinel the server
			// would log as ERROR|...|EOF.
			t.Fatalf("reader returned error; max-count early-stop sentinel must be swallowed: %v", err)
		}
		return processor
	}

	byteByByte := run(func(rf *readFile, ctx context.Context, l lcontext.LContext, p *captureProcessor, r regex.Regex) error {
		return rf.StartWithProcessor(ctx, l, p, r)
	})
	optimized := run(func(rf *readFile, ctx context.Context, l lcontext.LContext, p *captureProcessor, r regex.Regex) error {
		return rf.StartWithProcessorOptimized(ctx, l, p, r)
	})

	want := []string{"match 1\n", "match 2\n"}
	if !reflect.DeepEqual(optimized.lines, want) {
		t.Fatalf("optimized -max lines: got=%v want=%v", optimized.lines, want)
	}
	// Byte-identical -max output between the byte-by-byte and optimized paths.
	if !reflect.DeepEqual(byteByByte.lines, optimized.lines) {
		t.Fatalf("-max output differs between byte-by-byte and optimized: byteByByte=%v optimized=%v",
			byteByByte.lines, optimized.lines)
	}
}

// TestReadWithProcessorOptimizedMaxCountWithContextEarlyStop covers -m combined
// with after-context (-A). Here processWithContext returns the io.EOF sentinel
// from its maxReached branch (a distinct return site from plain -m: it fires on
// the NEXT match after the after-context window drains, not on the match that
// reaches the count). The optimized path must still swallow the sentinel
// (return nil), emit the after-context line, and stay byte-identical to the
// byte-by-byte path. Pre-fix the optimized run returns io.EOF and goes red.
func TestReadWithProcessorOptimizedMaxCountWithContextEarlyStop(t *testing.T) {
	const content = "x\nHIT one\ny\nHIT two\nz\nHIT three\n"
	// MaxCount 1 with AfterContext 1: emit the first match plus its single
	// trailing context line, then stop at the next match via the maxReached
	// sentinel.
	ltx := lcontext.LContext{MaxCount: 1, AfterContext: 1}

	run := func(start func(*readFile, context.Context, lcontext.LContext, *captureProcessor, regex.Regex) error) *captureProcessor {
		re, err := regex.New("HIT", regex.Default)
		if err != nil {
			t.Fatalf("build regex: %v", err)
		}
		filePath := writeProcessorTestFile(t, content)
		cat := NewCatFile(filePath, "glob-id", make(chan string, 1), defaultMaxLineLength)
		processor := &captureProcessor{}
		if err := start(&cat.readFile, context.Background(), ltx, processor, re); err != nil {
			t.Fatalf("reader returned error; max-count+context sentinel must be swallowed: %v", err)
		}
		return processor
	}

	byteByByte := run(func(rf *readFile, ctx context.Context, l lcontext.LContext, p *captureProcessor, r regex.Regex) error {
		return rf.StartWithProcessor(ctx, l, p, r)
	})
	optimized := run(func(rf *readFile, ctx context.Context, l lcontext.LContext, p *captureProcessor, r regex.Regex) error {
		return rf.StartWithProcessorOptimized(ctx, l, p, r)
	})

	want := []string{"HIT one\n", "y\n"}
	if !reflect.DeepEqual(optimized.lines, want) {
		t.Fatalf("optimized -m+context lines: got=%v want=%v", optimized.lines, want)
	}
	if !reflect.DeepEqual(byteByByte.lines, optimized.lines) {
		t.Fatalf("-m+context output differs between byte-by-byte and optimized: byteByByte=%v optimized=%v",
			byteByByte.lines, optimized.lines)
	}
}

// TestTailWithProcessorOptimizedMaxCountEarlyStop proves the follow/tail
// optimized path (tailWithProcessorOptimized) treats the io.EOF max-count early-stop
// sentinel as a clean stop (return nil) at ALL THREE of its processPartialLine
// call sites: the newline-terminated line site, the long-line split site, and
// the context-cancel trailing-partial cleanup site. Pre-fix each site returned
// io.EOF straight to the caller (logged as SERVER|...|ERROR|...|EOF), so every
// subtest goes red on the unfixed code. The reader is driven in-memory so the
// follow loop is deterministic; serverMessages is nil so warnAboutLongLine never
// blocks (it returns true immediately).
func TestTailWithProcessorOptimizedMaxCountEarlyStop(t *testing.T) {
	resetCommonLogger(t)

	newReadFile := func(maxLineLength int) readFile {
		return readFile{
			filePath:      "test.log",
			globID:        "glob-id",
			maxLineLength: maxLineLength,
		}
	}

	mustRegex := func(pattern string) regex.Regex {
		re, err := regex.New(pattern, regex.Default)
		if err != nil {
			t.Fatalf("build regex %q: %v", pattern, err)
		}
		return re
	}

	// runTail drives tailWithProcessorOptimized directly. fd is nil because the
	// truncate channel is never signaled, so f.truncated(fd) is never reached.
	runTail := func(t *testing.T, ctx context.Context, rf *readFile, input string,
		ltx lcontext.LContext, re regex.Regex) *captureProcessor {

		processor := &captureProcessor{}
		reader := bufio.NewReader(strings.NewReader(input))
		if err := rf.tailWithProcessorOptimized(ctx, nil, reader,
			make(chan struct{}), ltx, processor, re); err != nil {
			t.Fatalf("tail returned error; max-count sentinel must be swallowed: %v", err)
		}
		return processor
	}

	t.Run("newline terminated line site", func(t *testing.T) {
		rf := newReadFile(defaultMaxLineLength)
		// The second complete (newline-terminated) match hits the count and stops
		// via the newline branch's processPartialLine call.
		p := runTail(t, context.Background(), &rf, "match1\nmatch2\nmatch3\n",
			lcontext.LContext{MaxCount: 2}, mustRegex("match"))
		want := []string{"match1", "match2"}
		if !reflect.DeepEqual(p.lines, want) {
			t.Fatalf("lines: got=%v want=%v", p.lines, want)
		}
	})

	t.Run("long line split site", func(t *testing.T) {
		rf := newReadFile(4)
		// "aa\n" reaches count 1 via the newline branch; the un-terminated 6-byte
		// "aaaaaa" exceeds the 4-byte line limit and is flushed by the long-line
		// branch, reaching count 2 (max) at that site.
		p := runTail(t, context.Background(), &rf, "aa\naaaaaa",
			lcontext.LContext{MaxCount: 2}, mustRegex("a"))
		want := []string{"aa", "aaaaaa"}
		if !reflect.DeepEqual(p.lines, want) {
			t.Fatalf("lines: got=%v want=%v", p.lines, want)
		}
	})

	t.Run("context cancel trailing partial site", func(t *testing.T) {
		rf := newReadFile(defaultMaxLineLength)
		// The final "match2" has no trailing newline, so it stays buffered as a
		// partial line. A pre-canceled context routes it through the ctx.Done
		// cleanup branch, where it reaches count 2 (max). The 64KB read buffer
		// consumes the whole 13-byte input in one Read (err==nil), so the loop
		// reaches the bottom ctx.Done select with the partial line still pending.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p := runTail(t, ctx, &rf, "match1\nmatch2",
			lcontext.LContext{MaxCount: 2}, mustRegex("match"))
		want := []string{"match1", "match2"}
		if !reflect.DeepEqual(p.lines, want) {
			t.Fatalf("lines: got=%v want=%v", p.lines, want)
		}
	})
}

// nonRecyclingErrorProcessor returns an error WITHOUT recycling the buffer, so a
// test can observe whether filteringProcessor wrongly recycles a buffer whose
// ownership it already transferred to the processor. RecycleBytesBuffer calls
// buf.Reset(), so a stray recycle on the error path clears the payload - which
// this processor's caller can then detect.
type nonRecyclingErrorProcessor struct {
	err error
}

func (p nonRecyclingErrorProcessor) ProcessLine(_ *bytes.Buffer, _ uint64, _ string) error {
	return p.err
}

func (p nonRecyclingErrorProcessor) Flush() error { return nil }

func (p nonRecyclingErrorProcessor) Close() error { return nil }

// recyclingErrorProcessor mimics the real fs-path processors (DirectLineProcessor
// and AggregateProcessor): it recycles the buffer AND returns an error,
// exactly as DirectLineProcessor does when WriteLineData fails on a client
// disconnect / broken pipe. If filteringProcessor also recycled on error, the
// same buffer would be Put into the shared pool twice.
type recyclingErrorProcessor struct {
	err error
}

func (p recyclingErrorProcessor) ProcessLine(b *bytes.Buffer, _ uint64, _ string) error {
	pool.RecycleBytesBuffer(b)
	return p.err
}

func (p recyclingErrorProcessor) Flush() error { return nil }

func (p recyclingErrorProcessor) Close() error { return nil }

// TestFilteringProcessorDoesNotDoubleRecycleOnError is the regression guard for
// the yu0 production data race on the FILE read path (dcat/dgrep/dtail), the same
// class of bug fixed for the journal path in bt0 (1fe127a). The line.Processor
// contract transfers rawLine ownership to the processor, which recycles it on
// every return path (DirectLineProcessor and AggregateProcessor recycle
// unconditionally, even when ProcessLine returns a write error). If
// filteringProcessor also recycled on error, the same buffer would be returned to
// the shared pool.BytesBuffer twice; the pool would then hand one object to two
// Get callers whose concurrent writes race and corrupt data.
//
// The three caller-buffer error sites (ProcessFilteredLine simple case, the
// processWithContext after-context and matched-line sites) are checked with a
// nonRecyclingErrorProcessor: post-fix the buffer must be left untouched on error
// (payload survives). Pre-fix filteringProcessor called RecycleBytesBuffer -> the
// buffer was Reset and the payload vanished, so each sub-case goes red.
func TestFilteringProcessorDoesNotDoubleRecycleOnError(t *testing.T) {
	sinkErr := errors.New("processor stopped")
	matchAll := regex.NewNoop()
	noMatch, err := regex.New("NEEDLE_THAT_NEVER_MATCHES", regex.Default)
	if err != nil {
		t.Fatalf("build regex: %v", err)
	}

	tests := []struct {
		name    string
		ltx     lcontext.LContext
		re      regex.Regex
		payload string
		// primeAfter installs a pending after-context window so a non-matching line
		// is emitted through the after-context ProcessLine site.
		primeAfter bool
	}{
		{
			name:    "simple no-context site",
			ltx:     lcontext.LContext{},
			re:      matchAll,
			payload: "no-context-payload",
		},
		{
			name:       "context after-context site",
			ltx:        lcontext.LContext{AfterContext: 1},
			re:         noMatch,
			payload:    "after-context-payload",
			primeAfter: true,
		},
		{
			name:    "context matched-line site",
			ltx:     lcontext.LContext{AfterContext: 1},
			re:      matchAll,
			payload: "matched-line-payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var st stats
			fp := &filteringProcessor{
				processor: nonRecyclingErrorProcessor{err: sinkErr},
				re:        tt.re,
				ltx:       tt.ltx,
				stats:     &st,
				globID:    "glob-id",
			}
			if tt.primeAfter {
				fp.afterCount = 1
			}

			buf := pool.BytesBuffer.Get().(*bytes.Buffer)
			buf.Reset()
			buf.WriteString(tt.payload)

			if err := fp.ProcessFilteredLine(buf); !errors.Is(err, sinkErr) {
				t.Fatalf("ProcessFilteredLine error = %v, want %v", err, sinkErr)
			}
			if got := buf.String(); got != tt.payload {
				t.Fatalf("filteringProcessor recycled a buffer it does not own "+
					"(double-recycle regression): buf=%q want=%q", got, tt.payload)
			}

			// The processor did not recycle (test double), so recycle here to avoid
			// leaking the pooled buffer out of the test.
			pool.RecycleBytesBuffer(buf)
		})
	}
}

// TestProcessFilteredRawDoesNotDoubleRecycleOnError guards the fourth error site,
// the zero-copy fast path ProcessFilteredRaw, which acquires its own pooled buffer
// internally (so payload survival cannot be observed from outside). It uses a
// recyclingErrorProcessor that faithfully mimics DirectLineProcessor - recycle the
// buffer, then return a write error. Pre-fix, ProcessFilteredRaw recycled the same
// buffer a second time, Putting one pointer into the pool twice; a subsequent
// sweep of the pool then hands out that pointer more than once. Post-fix the
// buffer is Put exactly once and no duplicate appears.
func TestProcessFilteredRawDoesNotDoubleRecycleOnError(t *testing.T) {
	drainBytesBufferPool()

	sinkErr := errors.New("processor stopped")
	var st stats
	fp := &filteringProcessor{
		processor: recyclingErrorProcessor{err: sinkErr},
		re:        regex.NewNoop(),
		ltx:       lcontext.LContext{},
		stats:     &st,
		globID:    "glob-id",
	}

	if err := fp.ProcessFilteredRaw([]byte("match me\n")); !errors.Is(err, sinkErr) {
		t.Fatalf("ProcessFilteredRaw error = %v, want %v", err, sinkErr)
	}

	seen := make(map[*bytes.Buffer]int)
	for i := 0; i < 512; i++ {
		b := pool.BytesBuffer.Get().(*bytes.Buffer)
		seen[b]++
		if seen[b] > 1 {
			t.Fatalf("buffer %p observed in pool more than once: double-recycle "+
				"detected (Put twice into sync.Pool) on ProcessFilteredRaw error path", b)
		}
	}
}

func writeProcessorTestFile(t *testing.T, content string) string {
	t.Helper()

	filePath := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		t.Fatalf("unable to write test file: %v", err)
	}
	return filePath
}
