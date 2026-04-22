//go:build linux

package journal

import (
	"bytes"
	"context"
	"errors"
	"os"
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

	_, err := NewReader(nil, "journal", false, nil)
	if err == nil {
		t.Fatal("expected missing journalctl error")
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

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow reader returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follow reader did not stop after cancellation")
	}

	args := mock.Args(t)
	if !strings.Contains(args, "-f -n 0") {
		t.Fatalf("follow reader did not pass follow flags: %q", args)
	}
	if !mock.Terminated(t) {
		t.Fatal("journalctl did not observe SIGTERM")
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
	pid, err := strconv.Atoi(strings.TrimSpace(readFileString(t, mock.PIDFile)))
	if err != nil {
		t.Fatalf("parse fake journalctl pid: %v", err)
	}
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

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
