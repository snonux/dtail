package fs

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// recordingCounter is a ByteCounter that records what a read told it.
type recordingCounter struct {
	mu     sync.Mutex
	opens  []int64
	counts int64
}

func (c *recordingCounter) Opened(file *os.File) {
	info, err := file.Stat()
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens = append(c.opens, info.Size())
	c.counts = 0
}

func (c *recordingCounter) Add(n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts += n
}

// readCounted reads the file at path in cat mode with a recordingCounter and
// returns the counter and the lines read.
func readCounted(t *testing.T, path string) (*recordingCounter, int) {
	t.Helper()
	counter := &recordingCounter{}
	reader := mustNewReadFile(ReadOptions{
		Mode:     omode.CatClient,
		FilePath: path,
		GlobID:   "glob",
		Progress: counter,
	})
	processor := &captureProcessor{}
	if err := reader.Start(context.Background(), lcontext.LContext{}, processor, regex.NewNoop()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return counter, len(processor.lines)
}

// writeProgressFile writes data to a file of the given name and returns its
// path.
func writeProgressFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func progressTestLines() string {
	return strings.Repeat("a line of the progress test log\n", 20000)
}

// checkCounted checks that a whole read of the file at path counted its size
// on disk, told at its one open, and read lines lines.
func checkCounted(t *testing.T, path string, lines int) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	counter, got := readCounted(t, path)
	if got != lines {
		t.Fatalf("read %d lines, want %d", got, lines)
	}
	if len(counter.opens) != 1 || counter.opens[0] != info.Size() {
		t.Fatalf("opens = %v, want [%d]", counter.opens, info.Size())
	}
	if counter.counts != info.Size() {
		t.Fatalf("counted %d bytes, want the file size %d", counter.counts, info.Size())
	}
}

func TestProgressCountsPlainFile(t *testing.T) {
	checkCounted(t, writeProgressFile(t, "plain.log", []byte(progressTestLines())), 20000)
}

func TestProgressCountsCompressedBytesOfGzip(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(progressTestLines())); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	checkCounted(t, writeProgressFile(t, "log.gz", compressed.Bytes()), 20000)
}

func TestProgressNotCountedForFollowReads(t *testing.T) {
	reader := mustNewReadFile(ReadOptions{
		Mode:     omode.TailClient,
		FilePath: "/nonexistent",
		GlobID:   "glob",
		Progress: &recordingCounter{},
	})
	if reader.progress != nil {
		t.Fatal("a follow read counts progress")
	}
}
