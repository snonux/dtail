//go:build linux

package journal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	journaltest "github.com/mimecast/dtail/internal/io/journal/testhelper"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

type captureProcessor struct {
	lines []string
}

func (p *captureProcessor) ProcessLine(lineContent *bytes.Buffer, _ uint64, _ string) error {
	p.lines = append(p.lines, lineContent.String())
	pool.RecycleBytesBuffer(lineContent)
	return nil
}

func (p *captureProcessor) Flush() error {
	return nil
}

func (p *captureProcessor) Close() error {
	return nil
}

type errorProcessor struct {
	err error
}

func (p errorProcessor) ProcessLine(lineContent *bytes.Buffer, _ uint64, _ string) error {
	pool.RecycleBytesBuffer(lineContent)
	return p.err
}

func (p errorProcessor) Flush() error {
	return nil
}

func (p errorProcessor) Close() error {
	return nil
}

// nonRecyclingErrorProcessor returns an error without recycling the buffer, so a
// test can observe whether processorSink.Emit wrongly recycles a buffer it does
// not own.
type nonRecyclingErrorProcessor struct {
	err error
}

func (p nonRecyclingErrorProcessor) ProcessLine(_ *bytes.Buffer, _ uint64, _ string) error {
	return p.err
}

func (p nonRecyclingErrorProcessor) Flush() error { return nil }

func (p nonRecyclingErrorProcessor) Close() error { return nil }

// TestProcessorSinkEmitDoesNotRecycleOnError is the regression guard for the
// bt0 data race. The line.Processor contract transfers buffer ownership to the
// processor, which recycles it on every return path (the journal-path processors
// DirectLineProcessor and AggregateProcessor recycle unconditionally before
// returning a write error). If
// processorSink.Emit also recycled the buffer on error, the same buffer would be
// returned to the shared pool twice; the pool would then hand one object to two
// Get callers whose concurrent writes race and corrupt data. Emit must therefore
// leave the buffer untouched on error. RecycleBytesBuffer calls buf.Reset(), so
// a stray recycle would clear the payload — assert it survives.
func TestProcessorSinkEmitDoesNotRecycleOnError(t *testing.T) {
	buf := pool.BytesBuffer.Get().(*bytes.Buffer)
	buf.Reset()
	buf.WriteString("payload")

	sinkErr := errors.New("processor stopped")
	sink := processorSink{processor: nonRecyclingErrorProcessor{err: sinkErr}}

	err := sink.Emit(context.Background(), buf, 1, 100, "journal-id")
	if !errors.Is(err, sinkErr) {
		t.Fatalf("Emit error = %v, want %v", err, sinkErr)
	}
	if got := buf.String(); got != "payload" {
		t.Fatalf("processorSink.Emit recycled a buffer it does not own (double-recycle regression): buf=%q", got)
	}

	pool.RecycleBytesBuffer(buf)
}

func TestNewReaderFailsWhenJournalctlIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	reader, err := NewReader(nil, "journal", false, nil)
	if err == nil {
		t.Fatal("expected missing journalctl error")
	}
	if reader != nil {
		t.Fatalf("expected nil reader, got %#v", reader)
	}
	if !errors.Is(err, ErrJournalctlNotFound) {
		t.Fatalf("missing journalctl error = %v, want ErrJournalctlNotFound", err)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing journalctl error = %v, want exec.ErrNotFound", err)
	}
}

func TestStartReadsJournalctlOutputWithoutFollowFlags(t *testing.T) {
	mock := journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines: []string{"alpha", "beta"},
		},
	})

	reader, err := NewReader([]string{"-u", "ssh.service"}, "journal-id", false, make(chan string, 1))
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	processor := &captureProcessor{}
	if err := reader.StartWithProcessorOptimized(context.Background(), lcontext.LContext{},
		processor, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}

	want := []string{"alpha\n", "beta\n"}
	if !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("unexpected lines: got=%v want=%v", processor.lines, want)
	}

	args := mock.Args(t)
	if strings.Contains(args, "-f") || strings.Contains(args, "-n 0") {
		t.Fatalf("non-follow reader passed follow flags: %q", args)
	}
	if strings.TrimSpace(args) != "-u ssh.service" {
		t.Fatalf("unexpected journalctl args: %q", args)
	}
	if reader.Retry() {
		t.Fatal("non-follow reader should not retry")
	}
}

func TestStartFollowReadsLinesInOrder(t *testing.T) {
	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			FollowLines:    []string{"alpha", "beta", "gamma"},
			InterLineDelay: 5 * time.Millisecond,
		},
	})

	reader, err := NewReader([]string{"-u", "ssh.service"}, "journal-id", true, make(chan string, 8))
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	processor := &flushCountingProcessor{}
	done := make(chan error, 1)
	go func() {
		done <- reader.StartWithProcessorOptimized(ctx, lcontext.LContext{}, processor, regex.NewNoop())
	}()

	// Poll until all three follow lines have arrived (the reader appends from its
	// goroutine while following).
	deadline := time.After(2 * time.Second)
	var got []string
	for {
		got = processor.snapshot()
		if len(got) >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("follow reader did not deliver 3 lines: got=%v", got)
		case <-time.After(2 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("follow reader returned error after cancel: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("follow reader did not stop promptly after cancellation")
	}

	want := []string{"alpha\n", "beta\n", "gamma\n"}
	if !reflect.DeepEqual(got[:3], want) {
		t.Fatalf("unexpected follow lines: got=%v want=%v", got, want)
	}
}

func TestStartFollowPassesFollowFlagsAndTerminatesOnCancel(t *testing.T) {
	mock := journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines: []string{"ready"},
		},
	})

	reader, err := NewReader([]string{"-u", "ssh.service"}, "journal-id", true, make(chan string, 1))
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	processor := &flushCountingProcessor{}
	done := make(chan error, 1)
	go func() {
		done <- reader.StartWithProcessorOptimized(ctx, lcontext.LContext{}, processor, regex.NewNoop())
	}()

	// Wait for the first follow line to arrive before probing the process.
	deadline := time.After(2 * time.Second)
	for {
		got := processor.snapshot()
		if len(got) >= 1 {
			if got[0] != "ready\n" {
				t.Fatalf("unexpected first line: %q", got[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("follow reader did not deliver the first line")
		case <-time.After(2 * time.Millisecond):
		}
	}

	pid := mockPID(t, mock)
	if !processExists(pid) {
		t.Fatalf("fake journalctl process %d does not exist before cancel", pid)
	}

	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow reader returned error after cancel: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("follow reader did not stop promptly after cancellation")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("follow reader returned too slowly after cancel: %s", elapsed)
	}

	args := mock.Args(t)
	if !strings.Contains(args, "-f -n 0") {
		t.Fatalf("follow reader did not pass follow flags: %q", args)
	}
	if !mock.Terminated(t) {
		t.Fatal("journalctl did not observe SIGTERM")
	}
	if processExists(pid) {
		t.Fatalf("fake journalctl process %d still exists after reader returned", pid)
	}
	if !reader.Retry() {
		t.Fatal("follow reader should retry")
	}
}

func TestStartSurfacesStderrAsServerMessages(t *testing.T) {
	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines:  []string{"alpha"},
			Stderr: []string{"journal warning"},
		},
	})

	serverMessages := make(chan string, 1)
	reader, err := NewReader(nil, "journal-id", false, serverMessages)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	if err := reader.StartWithProcessorOptimized(context.Background(), lcontext.LContext{},
		&captureProcessor{}, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}

	select {
	case message := <-serverMessages:
		if message != "journalctl stderr: journal warning\n" {
			t.Fatalf("unexpected server message: %q", message)
		}
	default:
		t.Fatal("expected stderr server message")
	}
}

func TestStartReturnsExitErrorAndForwardsStderrOnNonZeroExit(t *testing.T) {
	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines:    []string{"before failure"},
			Stderr:   []string{"boom"},
			ExitCode: 17,
		},
	})

	serverMessages := make(chan string, 2)
	reader, err := NewReader(nil, "journal-id", false, serverMessages)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	processor := &captureProcessor{}
	err = reader.StartWithProcessorOptimized(context.Background(), lcontext.LContext{},
		processor, regex.NewNoop())
	if err == nil {
		t.Fatal("expected non-zero journalctl exit error")
	}
	if !strings.Contains(err.Error(), "journalctl failed") {
		t.Fatalf("unexpected non-zero exit error: %v", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %v, want exec.ExitError", err)
	}
	if got := exitErr.ExitCode(); got != 17 {
		t.Fatalf("exit code = %d, want 17", got)
	}
	if reader.Retry() {
		t.Fatal("non-follow reader should not retry after non-zero exit")
	}

	if want := []string{"before failure\n"}; !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("unexpected lines before failure: got=%v want=%v", processor.lines, want)
	}

	select {
	case message := <-serverMessages:
		if message != "journalctl stderr: boom\n" {
			t.Fatalf("unexpected server message: %q", message)
		}
	default:
		t.Fatal("expected stderr server message")
	}
}

func TestStartReadsLongJournalLine(t *testing.T) {
	const longLineLength = 70 * 1024

	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			LongLineLength: longLineLength,
		},
	})

	reader, err := NewReader(nil, "journal-id", false, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	processor := &captureProcessor{}
	if err := reader.StartWithProcessorOptimized(context.Background(), lcontext.LContext{},
		processor, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}

	want := []string{strings.Repeat("x", longLineLength) + "\n"}
	if !reflect.DeepEqual(processor.lines, want) {
		gotLen := 0
		if len(processor.lines) > 0 {
			gotLen = len(processor.lines[0])
		}
		t.Fatalf("unexpected long line: got len=%d want len=%d", gotLen, len(want[0]))
	}
}

func TestStartDropsPartialLineAtShutdown(t *testing.T) {
	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			PartialLine: "unterminated",
		},
	})

	reader, err := NewReader(nil, "journal-id", false, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	processor := &captureProcessor{}
	if err := reader.StartWithProcessorOptimized(context.Background(), lcontext.LContext{},
		processor, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}
	if len(processor.lines) != 0 {
		t.Fatalf("partial line without trailing newline was emitted: %v", processor.lines)
	}
}

func TestStartPreservesUTF8Lines(t *testing.T) {
	want := []string{"żółć 🚀\n", "東京 café\n"}
	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines: []string{strings.TrimSuffix(want[0], "\n"), strings.TrimSuffix(want[1], "\n")},
		},
	})

	reader, err := NewReader(nil, "journal-id", false, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	processor := &captureProcessor{}
	if err := reader.StartWithProcessorOptimized(context.Background(), lcontext.LContext{},
		processor, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}

	if !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("unexpected UTF-8 lines: got=%v want=%v", processor.lines, want)
	}
}

func TestConcurrentReadersDifferentUnitsDoNotInterfere(t *testing.T) {
	journaltest.InstallMock(t, journaltest.Scenario{
		Units: map[string]journaltest.Invocation{
			"alpha.service": {
				Lines: []string{"alpha-1", "alpha-2"},
			},
			"beta.service": {
				Lines: []string{"beta-1", "beta-2"},
			},
		},
	})

	type result struct {
		lines []string
		err   error
	}

	runReader := func(unit string) <-chan result {
		resultCh := make(chan result, 1)
		go func() {
			reader, err := NewReader([]string{"-u", unit}, unit, false, nil)
			if err != nil {
				resultCh <- result{err: err}
				return
			}
			processor := &captureProcessor{}
			err = reader.StartWithProcessorOptimized(context.Background(), lcontext.LContext{},
				processor, regex.NewNoop())
			resultCh <- result{lines: processor.lines, err: err}
		}()
		return resultCh
	}

	alphaCh := runReader("alpha.service")
	betaCh := runReader("beta.service")

	alpha := <-alphaCh
	beta := <-betaCh

	if alpha.err != nil {
		t.Fatalf("alpha reader: %v", alpha.err)
	}
	if beta.err != nil {
		t.Fatalf("beta reader: %v", beta.err)
	}
	if want := []string{"alpha-1\n", "alpha-2\n"}; !reflect.DeepEqual(alpha.lines, want) {
		t.Fatalf("alpha lines: got=%v want=%v", alpha.lines, want)
	}
	if want := []string{"beta-1\n", "beta-2\n"}; !reflect.DeepEqual(beta.lines, want) {
		t.Fatalf("beta lines: got=%v want=%v", beta.lines, want)
	}
}

func TestStartWithProcessorOptimizedAppliesRegexAndLocalContext(t *testing.T) {
	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines: []string{"before", "match", "context", "skip"},
		},
	})

	reader, err := NewReader(nil, "journal-id", false, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	re, err := regex.New("match", regex.Default)
	if err != nil {
		t.Fatalf("new regex: %v", err)
	}
	processor := &captureProcessor{}

	err = reader.StartWithProcessorOptimized(
		context.Background(),
		lcontext.LContext{BeforeContext: 1, AfterContext: 1, MaxCount: 1},
		processor,
		re,
	)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("start optimized reader: %v", err)
	}

	want := []string{"before\n", "match\n", "context\n"}
	if !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("unexpected processed lines: got=%v want=%v", processor.lines, want)
	}
}

// flushCountingProcessor records processed lines and how many times Flush was
// invoked, guarded by a mutex because the reader drives it from a goroutine.
type flushCountingProcessor struct {
	mu         sync.Mutex
	lines      []string
	flushCount int
}

func (p *flushCountingProcessor) ProcessLine(lineContent *bytes.Buffer, _ uint64, _ string) error {
	p.mu.Lock()
	p.lines = append(p.lines, lineContent.String())
	p.mu.Unlock()
	pool.RecycleBytesBuffer(lineContent)
	return nil
}

func (p *flushCountingProcessor) Flush() error {
	p.mu.Lock()
	p.flushCount++
	p.mu.Unlock()
	return nil
}

func (p *flushCountingProcessor) Close() error { return nil }

func (p *flushCountingProcessor) counts() (lines, flushes int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.lines), p.flushCount
}

// snapshot returns a copy of the processed lines so a test can inspect ordered
// content while the follow reader keeps appending from its goroutine.
func (p *flushCountingProcessor) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lines...)
}

// TestStartWithProcessorFollowFlushesEachLine is the regression guard for the
// output batching fix (task 0t0). A follow read blocks in r.run until journalctl
// is stopped, so a batching output writer would hold live lines in its 64KB
// buffer and the client would see nothing until the stream ends. The reader
// must therefore flush the processor after every line while following. If the
// per-line flush is dropped, flushCount stays at zero during the follow and
// this test fails.
func TestStartWithProcessorFollowFlushesEachLine(t *testing.T) {
	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			FollowLines:    []string{"one", "two", "three"},
			InterLineDelay: 5 * time.Millisecond,
		},
	})

	reader, err := NewReader([]string{"-u", "ssh.service"}, "journal-id", true, make(chan string, 8))
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	processor := &flushCountingProcessor{}
	done := make(chan error, 1)
	go func() {
		done <- reader.StartWithProcessorOptimized(ctx, lcontext.LContext{}, processor, regex.NewNoop())
	}()

	// While the follow is still live, each of the three lines must trigger a
	// flush. Poll until at least three flushes are observed.
	deadline := time.After(2 * time.Second)
	for {
		lines, flushes := processor.counts()
		if lines >= 3 && flushes >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("follow reader did not flush per line: lines=%d flushes=%d", lines, flushes)
		case <-time.After(2 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("follow reader returned error after cancel: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("follow reader did not stop promptly after cancellation")
	}
}

// TestStartWithProcessorNonFollowFlushesOnceAtEnd pins the complementary
// behavior: a non-follow snapshot read keeps the batching benefit and only
// flushes once, at the end, rather than per line.
func TestStartWithProcessorNonFollowFlushesOnceAtEnd(t *testing.T) {
	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines: []string{"a", "b", "c", "d"},
		},
	})

	reader, err := NewReader(nil, "journal-id", false, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	processor := &flushCountingProcessor{}
	if err := reader.StartWithProcessorOptimized(context.Background(),
		lcontext.LContext{}, processor, regex.NewNoop()); err != nil {
		t.Fatalf("start optimized reader: %v", err)
	}

	lines, flushes := processor.counts()
	if lines != 4 {
		t.Fatalf("expected 4 processed lines, got %d", lines)
	}
	// Only the single terminal flush in runWithProcessor should fire.
	if flushes != 1 {
		t.Fatalf("expected exactly 1 flush for a non-follow read, got %d", flushes)
	}
}

func TestStartWithProcessorErrorTerminatesJournalctl(t *testing.T) {
	mock := journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines:    []string{"ready"},
			HoldOpen: true,
		},
	})

	reader, err := NewReader(nil, "journal-id", false, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	processorErr := errors.New("processor stopped")
	done := make(chan error, 1)
	go func() {
		done <- reader.StartWithProcessor(
			context.Background(),
			lcontext.LContext{},
			errorProcessor{err: processorErr},
			regex.NewNoop(),
		)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, processorErr) {
			t.Fatalf("unexpected reader error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader hung after processor error")
	}

	if !mock.Terminated(t) {
		t.Fatal("journalctl did not observe SIGTERM")
	}
}

func TestStartWithProcessorErrorKillsTermIgnoringJournalctl(t *testing.T) {
	mock := journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines:         []string{"ready"},
			HoldOpen:      true,
			IgnoreSIGTERM: true,
		},
	})

	reader, err := NewReader(nil, "journal-id", false, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	processorErr := errors.New("processor stopped")
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- reader.StartWithProcessor(
			context.Background(),
			lcontext.LContext{},
			errorProcessor{err: processorErr},
			regex.NewNoop(),
		)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, processorErr) {
			t.Fatalf("unexpected reader error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader hung after TERM-ignoring journalctl")
	}

	if elapsed := time.Since(started); elapsed < processTerminateGrace {
		t.Fatalf("reader returned before kill grace elapsed: %s", elapsed)
	}
	if !mock.Terminated(t) {
		t.Fatal("journalctl did not observe SIGTERM before kill")
	}
	pid := mockPID(t, mock)
	if processExists(pid) {
		t.Fatalf("fake journalctl process %d still exists after reader returned", pid)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func mockPID(t *testing.T, mock *journaltest.Mock) int {
	t.Helper()

	pid, err := strconv.Atoi(strings.TrimSpace(readFileString(t, mock.PIDFile)))
	if err != nil {
		t.Fatalf("parse fake journalctl pid: %v", err)
	}
	return pid
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
