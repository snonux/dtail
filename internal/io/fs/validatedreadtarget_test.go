package fs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

func TestValidatedCatFileStartWithProcessorOptimizedReadsAllLines(t *testing.T) {
	resetCommonLogger(t)

	filePath := writeProcessorTestFile(t, "alpha\nbeta\n")
	target := mustValidatedReadTarget(t, filePath)
	re := regex.NewNoop()

	cat := NewValidatedCatFile(filePath, target, "glob-id", make(chan string, 1), defaultMaxLineLength)
	processor := &captureProcessor{}

	if err := cat.readFile.StartWithProcessorOptimized(
		context.Background(),
		lcontext.LContext{},
		processor,
		re,
	); err != nil {
		t.Fatalf("validated optimized reader start failed: %v", err)
	}

	want := []string{"alpha\n", "beta\n"}
	if !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("unexpected processed lines: got=%v want=%v", processor.lines, want)
	}
}

func TestValidatedReadTargetOpenRejectsEscapingSymlinkSwap(t *testing.T) {
	resetCommonLogger(t)

	baseDir := t.TempDir()
	rootDir := filepath.Join(baseDir, "root")
	outsideDir := filepath.Join(baseDir, "outside")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		t.Fatalf("mkdir root dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}

	filePath := filepath.Join(rootDir, "app.log")
	if err := os.WriteFile(filePath, []byte("alpha\n"), 0600); err != nil {
		t.Fatalf("write app log: %v", err)
	}
	escapePath := filepath.Join(outsideDir, "secret.log")
	if err := os.WriteFile(escapePath, []byte("secret\n"), 0600); err != nil {
		t.Fatalf("write secret log: %v", err)
	}

	target := mustValidatedReadTarget(t, filePath)

	if err := os.Remove(filePath); err != nil {
		t.Fatalf("remove app log: %v", err)
	}
	relativeEscape, err := filepath.Rel(rootDir, escapePath)
	if err != nil {
		t.Fatalf("relative escape path: %v", err)
	}
	if err := os.Symlink(relativeEscape, filePath); err != nil {
		t.Fatalf("symlink escape path: %v", err)
	}

	if _, err := target.Open(); err == nil {
		t.Fatal("expected rooted open to reject escaping symlink swap")
	}
}

func TestValidatedReadTargetOpenRejectsSameRootSymlinkSwap(t *testing.T) {
	resetCommonLogger(t)

	baseDir := t.TempDir()
	filePath := filepath.Join(baseDir, "app.log")
	if err := os.WriteFile(filePath, []byte("alpha\n"), 0600); err != nil {
		t.Fatalf("write app log: %v", err)
	}
	otherPath := filepath.Join(baseDir, "other.log")
	if err := os.WriteFile(otherPath, []byte("other\n"), 0600); err != nil {
		t.Fatalf("write other log: %v", err)
	}

	target := mustValidatedReadTarget(t, filePath)

	if err := os.Remove(filePath); err != nil {
		t.Fatalf("remove app log: %v", err)
	}
	if err := os.Symlink(filepath.Base(otherPath), filePath); err != nil {
		t.Fatalf("symlink other log: %v", err)
	}

	_, err := target.Open()
	if err == nil {
		t.Fatal("expected rooted open to reject same-root symlink swap")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestValidatedTailFileTruncatedReopenDetectsTruncation(t *testing.T) {
	resetCommonLogger(t)

	filePath := writeProcessorTestFile(t, "alpha\nbeta\n")
	target := mustValidatedReadTarget(t, filePath)

	tail := NewValidatedTailFile(filePath, target, "glob-id", make(chan string, 1), defaultMaxLineLength)
	fd, err := target.Open()
	if err != nil {
		t.Fatalf("open validated target: %v", err)
	}
	defer fd.Close()

	if _, err := fd.Seek(0, io.SeekEnd); err != nil {
		t.Fatalf("seek end: %v", err)
	}
	if err := os.Truncate(filePath, 1); err != nil {
		t.Fatalf("truncate file: %v", err)
	}

	isTruncated, err := tail.readFile.truncated(fd)
	if !isTruncated {
		t.Fatal("expected truncation to be detected")
	}
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncation error, got %v", err)
	}
}

func mustValidatedReadTarget(t *testing.T, path string) ValidatedReadTarget {
	t.Helper()

	absolutePath, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	target, err := NewValidatedReadTarget(absolutePath)
	if err != nil {
		t.Fatalf("create validated target: %v", err)
	}

	return target
}

func resetCommonLogger(t *testing.T) {
	t.Helper()

	originalLogger := dlog.Common
	dlog.Common = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Common = originalLogger
	})
}
