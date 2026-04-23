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
	"syscall"
	"testing"
	"time"

	journaltest "github.com/mimecast/dtail/internal/io/journal/testhelper"
	"github.com/mimecast/dtail/internal/io/line"
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

	lines := make(chan *line.Line, 2)
	if err := reader.Start(context.Background(), lcontext.LContext{}, lines, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}

	got := drainLines(t, lines)
	want := []string{"alpha\n", "beta\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected lines: got=%v want=%v", got, want)
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

	lines := make(chan *line.Line, 3)
	done := make(chan error, 1)
	go func() {
		done <- reader.Start(ctx, lcontext.LContext{}, lines, regex.NewNoop())
	}()

	got := make([]string, 0, 3)
	for len(got) < 3 {
		next := readLine(t, lines)
		got = append(got, next.Content.String())
		pool.RecycleBytesBuffer(next.Content)
		next.Recycle()
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow reader returned error after cancel: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("follow reader did not stop promptly after cancellation")
	}

	want := []string{"alpha\n", "beta\n", "gamma\n"}
	if !reflect.DeepEqual(got, want) {
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
	lines := make(chan *line.Line, 1)
	done := make(chan error, 1)
	go func() {
		done <- reader.Start(ctx, lcontext.LContext{}, lines, regex.NewNoop())
	}()

	first := readLine(t, lines)
	if got := first.Content.String(); got != "ready\n" {
		t.Fatalf("unexpected first line: %q", got)
	}
	pool.RecycleBytesBuffer(first.Content)
	first.Recycle()

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

	lines := make(chan *line.Line, 1)
	if err := reader.Start(context.Background(), lcontext.LContext{}, lines, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}
	recycleLines(t, lines)

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

	lines := make(chan *line.Line, 1)
	err = reader.Start(context.Background(), lcontext.LContext{}, lines, regex.NewNoop())
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

	gotLines := drainLines(t, lines)
	if want := []string{"before failure\n"}; !reflect.DeepEqual(gotLines, want) {
		t.Fatalf("unexpected lines before failure: got=%v want=%v", gotLines, want)
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

	lines := make(chan *line.Line, 1)
	if err := reader.Start(context.Background(), lcontext.LContext{}, lines, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}

	got := drainLines(t, lines)
	want := []string{strings.Repeat("x", longLineLength) + "\n"}
	if !reflect.DeepEqual(got, want) {
		gotLen := 0
		if len(got) > 0 {
			gotLen = len(got[0])
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

	lines := make(chan *line.Line, 1)
	if err := reader.Start(context.Background(), lcontext.LContext{}, lines, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}
	if len(lines) != 0 {
		recycleLines(t, lines)
		t.Fatal("partial line without trailing newline was emitted")
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

	lines := make(chan *line.Line, len(want))
	if err := reader.Start(context.Background(), lcontext.LContext{}, lines, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}

	if got := drainLines(t, lines); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected UTF-8 lines: got=%v want=%v", got, want)
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
			lines := make(chan *line.Line, 2)
			err = reader.Start(context.Background(), lcontext.LContext{}, lines, regex.NewNoop())
			resultCh <- result{lines: collectAvailableLines(lines), err: err}
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

func drainLines(t *testing.T, lines chan *line.Line) []string {
	t.Helper()

	var got []string
	for len(lines) > 0 {
		next := readLine(t, lines)
		got = append(got, next.Content.String())
		pool.RecycleBytesBuffer(next.Content)
		next.Recycle()
	}
	return got
}

func collectAvailableLines(lines chan *line.Line) []string {
	var got []string
	for len(lines) > 0 {
		next := <-lines
		got = append(got, next.Content.String())
		pool.RecycleBytesBuffer(next.Content)
		next.Recycle()
	}
	return got
}

func recycleLines(t *testing.T, lines chan *line.Line) {
	t.Helper()

	for len(lines) > 0 {
		next := readLine(t, lines)
		pool.RecycleBytesBuffer(next.Content)
		next.Recycle()
	}
}

func readLine(t *testing.T, lines <-chan *line.Line) *line.Line {
	t.Helper()

	select {
	case next := <-lines:
		return next
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for line")
		return nil
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
