package fs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

type captureProcessor struct {
	lines      []string
	errAtLine  int
	processErr error
	flushErr   error
}

func (p *captureProcessor) ProcessLine(lineContent *bytes.Buffer, _ uint64, _ string) error {
	p.lines = append(p.lines, lineContent.String())
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

func TestStartVariantsExitWhenContextCanceledDuringLongLineWarning(t *testing.T) {
	resetCommonLogger(t)

	filePath := writeProcessorTestFile(t, strings.Repeat("a", 8))
	re := regex.NewNoop()

	tests := []struct {
		name  string
		start func(*readFile, context.Context, lcontext.LContext, *captureProcessor, regex.Regex) error
	}{
		{
			name: "standard reader",
			start: func(rf *readFile, ctx context.Context, ltx lcontext.LContext, p *captureProcessor, re regex.Regex) error {
				return rf.Start(ctx, ltx, make(chan *line.Line, 1), re)
			},
		},
		{
			name: "processor reader",
			start: func(rf *readFile, ctx context.Context, ltx lcontext.LContext, p *captureProcessor, re regex.Regex) error {
				return rf.StartWithProcessor(ctx, ltx, p, re)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := NewCatFile(filePath, "glob-id", make(chan string), 1)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			errCh := make(chan error, 1)
			go func() {
				errCh <- tt.start(&cat.readFile, ctx, lcontext.LContext{}, &captureProcessor{}, re)
			}()

			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("expected canceled start to exit cleanly, got %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("start did not return after context cancellation")
			}
		})
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

func writeProcessorTestFile(t *testing.T, content string) string {
	t.Helper()

	filePath := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		t.Fatalf("unable to write test file: %v", err)
	}
	return filePath
}
