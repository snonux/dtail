package fs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mimecast/dtail/internal/config"
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
	setServerConfigForProcessorTests(t)

	filePath := writeProcessorTestFile(t, "alpha\nbeta\n")
	re := regex.NewNoop()

	cat := NewCatFile(filePath, "glob-id", make(chan string, 1))
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
	setServerConfigForProcessorTests(t)

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
			cat := NewCatFile(missingFile, "glob-id", make(chan string, 1))
			err := tt.start(&cat.readFile, context.Background(), lcontext.LContext{}, &captureProcessor{}, re)
			if err == nil {
				t.Fatalf("expected error for missing file")
			}
		})
	}
}

func TestStartWithProcessorOptimizedPropagatesProcessError(t *testing.T) {
	setServerConfigForProcessorTests(t)

	filePath := writeProcessorTestFile(t, "alpha\nbeta\n")
	re := regex.NewNoop()
	expectedErr := errors.New("processor failure")

	cat := NewCatFile(filePath, "glob-id", make(chan string, 1))
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

func setServerConfigForProcessorTests(t *testing.T) {
	t.Helper()

	previousServer := config.Server
	config.Server = &config.ServerConfig{
		MaxLineLength: 1024 * 1024,
	}
	t.Cleanup(func() {
		config.Server = previousServer
	})
}

func writeProcessorTestFile(t *testing.T, content string) string {
	t.Helper()

	filePath := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		t.Fatalf("unable to write test file: %v", err)
	}
	return filePath
}
