package fs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// positionProcessor records lines and the positions a follow reader reports.
type positionProcessor struct {
	mu      sync.Mutex
	lines   []string
	offsets []int64
	files   []os.FileInfo
}

func (p *positionProcessor) ProcessLine(buf *bytes.Buffer, _ uint64, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, buf.String())
	pool.RecycleBytesBuffer(buf)
	return nil
}

func (p *positionProcessor) Flush() error { return nil }
func (p *positionProcessor) Close() error { return nil }

func (p *positionProcessor) LinesEndAt(offset int64, file os.FileInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.offsets = append(p.offsets, offset)
	p.files = append(p.files, file)
}

func (p *positionProcessor) state() ([]string, []int64, []os.FileInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lines...), append([]int64(nil), p.offsets...),
		append([]os.FileInfo(nil), p.files...)
}

func appendToFile(t *testing.T, path, text string) {
	t.Helper()
	fd, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	if _, err := fd.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFollowReaderReportsWhereItsLinesEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "positions.log")
	existing := "existing\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := newFollowReadFile(path, "glob", nil, defaultMaxLineLength, testLogger)
	processor := &positionProcessor{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- reader.Start(ctx, lcontext.LContext{}, processor, regex.NewNoop()) }()
	defer func() {
		cancel()
		<-done
	}()

	// The reader seeks to the end first; append until it reads what follows.
	waitUntil(t, "the reader to pick up a line", func() bool {
		appendToFile(t, path, "probe\n")
		time.Sleep(20 * time.Millisecond)
		lines, _, _ := processor.state()
		return len(lines) > 0
	})
	// An unfinished line must not count as fed.
	appendToFile(t, path, "complete\nunfinish")
	waitUntil(t, "the complete line", func() bool {
		lines, _, _ := processor.state()
		return len(lines) > 0 && lines[len(lines)-1] == "complete"
	})
	time.Sleep(50 * time.Millisecond)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantOffset := int64(strings.LastIndexByte(string(data), '\n') + 1)
	_, offsets, files := processor.state()
	if len(offsets) == 0 {
		t.Fatal("the reader reported no position")
	}
	if got := offsets[len(offsets)-1]; got != wantOffset {
		t.Errorf("last reported offset = %d, want %d (just past the last complete line)", got, wantOffset)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(files[len(files)-1], info) {
		t.Error("the reported file identity is not the file being read")
	}
}

func TestReadersWithoutAnObserverOrForCompressedFilesReportNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.log")
	if err := os.WriteFile(path, []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()

	plain := newFollowReadFile(path, "glob", nil, defaultMaxLineLength, testLogger)
	if reporter, err := plain.newPositionReporter(fd, nil, &captureProcessor{}); reporter != nil || err != nil {
		t.Errorf("reporter for a processor without LinesEndAt = %v, %v, want nil", reporter, err)
	}
	gz := newFollowReadFile(path+".gz", "glob", nil, defaultMaxLineLength, testLogger)
	if reporter, err := gz.newPositionReporter(fd, nil, &positionProcessor{}); reporter != nil || err != nil {
		t.Errorf("reporter for a compressed file = %v, %v, want nil", reporter, err)
	}
	if reporter, err := plain.newPositionReporter(nil, nil, &positionProcessor{}); reporter != nil || err != nil {
		t.Errorf("reporter for the stdin pipe = %v, %v, want nil", reporter, err)
	}
	var none *positionReporter
	if err := none.report(0); err != nil {
		t.Errorf("a nil reporter reported an error: %v", err)
	}
}

func TestReplaceTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.log")
	other := filepath.Join(dir, "other.log")
	for _, p := range []string{path, other} {
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target, err := NewValidatedReadTarget(path)
	if err != nil {
		t.Fatal(err)
	}
	sameFile, err := NewValidatedReadTarget(path)
	if err != nil {
		t.Fatal(err)
	}
	otherTarget, err := NewValidatedReadTarget(other)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := NewValidatedJournalTarget("journal:dtail.service")
	if err != nil {
		t.Fatal(err)
	}

	withTarget := mustNewReadFile(ReadOptions{Mode: omode.TailClient, Target: &target, FilePath: path})
	if err := withTarget.ReplaceTarget(sameFile); err != nil {
		t.Errorf("replacing with a target for the same file: %v", err)
	}
	if withTarget.validatedTarget.Load().ResolvedPath() != path {
		t.Error("the replaced target does not name the file")
	}
	if err := withTarget.ReplaceTarget(otherTarget); err == nil {
		t.Error("a target for another file was accepted")
	}
	if err := withTarget.ReplaceTarget(journal); err == nil {
		t.Error("a journal target was accepted")
	}
	withoutTarget := mustNewReadFile(ReadOptions{Mode: omode.TailClient, FilePath: path})
	if err := withoutTarget.ReplaceTarget(target); err == nil {
		t.Error("a reader without a target was given one")
	}
}
