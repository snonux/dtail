package fs

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

func writeStartOffsetTestFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewReadFileValidatesStartOffset(t *testing.T) {
	tests := []struct {
		name    string
		options ReadOptions
		wantErr string
	}{
		{"zero offset", ReadOptions{FilePath: "/tmp/a.log"}, ""},
		{"positive offset", ReadOptions{FilePath: "/tmp/a.log", StartOffset: 10}, ""},
		{"zero offset with seek EOF", ReadOptions{FilePath: "/tmp/a.log", SeekEOF: true}, ""},
		{"zero offset on a compressed file", ReadOptions{FilePath: "/tmp/a.log.gz"}, ""},
		{"negative offset", ReadOptions{FilePath: "/tmp/a.log", StartOffset: -1}, "negative"},
		{"offset with seek EOF", ReadOptions{FilePath: "/tmp/a.log", StartOffset: 10, SeekEOF: true},
			"mutually exclusive"},
		{"offset on the stdin pipe", ReadOptions{GlobID: "-", StartOffset: 10}, "stdin pipe"},
		{"offset on a gz file", ReadOptions{FilePath: "/tmp/a.log.gz", StartOffset: 10}, "compressed"},
		{"offset on a gzip file", ReadOptions{FilePath: "/tmp/a.log.gzip", StartOffset: 10}, "compressed"},
		{"offset on a zst file", ReadOptions{FilePath: "/tmp/a.log.zst", StartOffset: 10}, "compressed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := tt.options
			options.Mode = omode.TailClient
			reader, err := NewReadFile(options)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("NewReadFile() error = %v, want nil", err)
				}
				if reader.initialOffset != tt.options.StartOffset {
					t.Fatalf("initialOffset = %d, want %d", reader.initialOffset, tt.options.StartOffset)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("NewReadFile() error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestStartOffsetSnapshotReadStartsAtOffset(t *testing.T) {
	path := writeStartOffsetTestFile(t, "offset.log", "alpha\nbeta\ngamma\n")
	reader := mustNewReadFile(ReadOptions{
		Mode:        omode.CatClient,
		FilePath:    path,
		GlobID:      "glob",
		StartOffset: int64(len("alpha\n")),
		Logger:      testLogger,
	})
	processor := &captureProcessor{}
	if err := reader.Start(context.Background(), lcontext.LContext{}, processor, regex.NewNoop()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Line numbers count from the offset, as they count from EOF for SeekEOF.
	if want := []string{"beta\n", "gamma\n"}; !reflect.DeepEqual(processor.lines, want) {
		t.Errorf("lines = %q, want %q", processor.lines, want)
	}
	if want := []uint64{1, 2}; !reflect.DeepEqual(processor.lineNums, want) {
		t.Errorf("line numbers = %v, want %v", processor.lineNums, want)
	}
}

func readAllFromNewReader(t *testing.T, reader *ReadFile) string {
	t.Helper()
	bufReader, fd, decompressor, err := reader.makeFileReader()
	if err != nil {
		t.Fatalf("makeFileReader() error = %v", err)
	}
	defer func() { _ = fd.Close() }()
	if decompressor != nil {
		defer func() { _ = decompressor.Close() }()
	}
	data, err := io.ReadAll(bufReader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestStartOffsetAppliesToFirstSuccessfulOpenOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "late.log")
	reader := mustNewReadFile(ReadOptions{
		Mode:        omode.TailClient,
		FilePath:    path,
		GlobID:      "glob",
		StartOffset: int64(len("alpha\n")),
		Logger:      testLogger,
	})

	// A failed first open must keep the offset for the retry.
	if _, fd, _, err := reader.makeFileReader(); err == nil {
		_ = fd.Close()
		t.Fatal("makeFileReader() on a missing file succeeded")
	}
	if reader.initialOffset == 0 {
		t.Fatal("a failed open cleared the start offset")
	}

	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readAllFromNewReader(t, reader); got != "beta\n" {
		t.Errorf("first successful open read %q, want %q", got, "beta\n")
	}
	// A reopen, e.g. after rotation, starts at the beginning as usual.
	if got := readAllFromNewReader(t, reader); got != "alpha\nbeta\n" {
		t.Errorf("reopen read %q, want %q", got, "alpha\nbeta\n")
	}
}

// cancelOnLineProcessor records lines and cancels the read once it has want.
type cancelOnLineProcessor struct {
	captureProcessor
	want   int
	cancel context.CancelFunc
}

func (p *cancelOnLineProcessor) ProcessLine(lineContent *bytes.Buffer, lineNum uint64, sourceID string) error {
	err := p.captureProcessor.ProcessLine(lineContent, lineNum, sourceID)
	if len(p.lines) >= p.want {
		p.cancel()
	}
	return err
}

func TestStartOffsetBeyondFileSizeRewindsFollowRead(t *testing.T) {
	path := writeStartOffsetTestFile(t, "short.log", "alpha\nbeta\n")
	reader := mustNewReadFile(ReadOptions{
		Mode:        omode.TailClient,
		FilePath:    path,
		GlobID:      "glob",
		StartOffset: 1000,
		Logger:      testLogger,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	processor := &cancelOnLineProcessor{want: 2, cancel: cancel}
	if err := reader.Start(ctx, lcontext.LContext{}, processor, regex.NewNoop()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// The file is shorter than the offset, so the follow reader treats it as
	// truncated and reads it from the beginning.
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(processor.lines, want) {
		t.Errorf("lines = %q, want %q (context error %v)", processor.lines, want, ctx.Err())
	}
}
