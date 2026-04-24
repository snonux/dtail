package pgo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteGrowingLog verifies that writeGrowingLog writes the expected log
// lines to the target file using pure Go I/O.  The test exercises paths that
// contain spaces and shell metacharacters to confirm that no shell
// interpolation takes place (i.e. the file is created at the literal path and
// not misinterpreted as a shell command).
func TestWriteGrowingLog(t *testing.T) {
	// Use a temporary directory with spaces and shell metacharacters in its
	// name to ensure the pure-Go writer handles them safely.
	base := t.TempDir()
	specialDir := filepath.Join(base, "profile dir with spaces & $(echo injected)")
	if err := os.MkdirAll(specialDir, 0755); err != nil {
		t.Fatalf("creating special dir: %v", err)
	}

	logPath := filepath.Join(specialDir, "growing.log")

	// Pre-create an empty file as the caller would.
	if err := os.WriteFile(logPath, []byte{}, 0644); err != nil {
		t.Fatalf("creating empty log: %v", err)
	}

	// Run the writer to completion; pass a channel that is never closed so
	// the writer runs all 200 iterations.
	stop := make(chan struct{})
	writeGrowingLog(logPath, stop)
	close(stop)

	// Verify the written content.
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("opening log: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineCount := 0
	levels := []string{"INFO", "WARN", "ERROR", "DEBUG"}

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		// Each line must contain the expected log level for its position.
		expectedLevel := levels[(lineCount-1)%len(levels)]
		if !strings.Contains(line, expectedLevel) {
			t.Errorf("line %d: expected level %s, got: %s", lineCount, expectedLevel, line)
		}

		// Each line must contain the line number.
		expectedNum := fmt.Sprintf("number %d ", lineCount)
		if !strings.Contains(line, expectedNum) {
			t.Errorf("line %d: expected %q in line, got: %s", lineCount, expectedNum, line)
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning log: %v", err)
	}

	const wantLines = 200
	if lineCount != wantLines {
		t.Errorf("got %d lines, want %d", lineCount, wantLines)
	}
}

// TestWriteGrowingLogStopsEarly verifies that writeGrowingLog respects the
// stop channel and exits before writing all 200 lines when requested.
func TestWriteGrowingLogStopsEarly(t *testing.T) {
	base := t.TempDir()
	logPath := filepath.Join(base, "growing.log")

	if err := os.WriteFile(logPath, []byte{}, 0644); err != nil {
		t.Fatalf("creating empty log: %v", err)
	}

	// Close the stop channel immediately so the writer should abort early.
	stop := make(chan struct{})
	close(stop)

	writeGrowingLog(logPath, stop)

	// The file should exist (even if empty or only partially written).
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file should exist after early stop: %v", err)
	}

	// The writer may have written 0 lines (stop closed before first line).
	// The important invariant is that it did NOT write all 200 lines without
	// checking the channel.
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("opening log: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
	}

	const maxExpected = 200
	if lineCount >= maxExpected {
		t.Errorf("expected fewer than %d lines when stopped early, got %d", maxExpected, lineCount)
	}
}

// TestWriteGrowingLogPathWithMetacharacters ensures that the log file is
// created at the literal filesystem path even when the path contains
// characters that would be dangerous in a shell context (semicolons, pipes,
// backticks, dollar signs, etc.).
func TestWriteGrowingLogPathWithMetacharacters(t *testing.T) {
	base := t.TempDir()

	// Directory name containing characters that would cause shell injection.
	dangerousDir := filepath.Join(base, "dir;rm -rf /tmp/pwned|echo`id`$HOME")
	if err := os.MkdirAll(dangerousDir, 0755); err != nil {
		t.Fatalf("creating dangerous dir: %v", err)
	}

	logPath := filepath.Join(dangerousDir, "test.log")
	if err := os.WriteFile(logPath, []byte{}, 0644); err != nil {
		t.Fatalf("pre-creating log: %v", err)
	}

	stop := make(chan struct{})
	writeGrowingLog(logPath, stop)
	close(stop)

	// The file at the literal path must exist and contain data.
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("log at literal path must exist: %v", err)
	}
	if info.Size() == 0 {
		t.Error("log file must be non-empty after writeGrowingLog")
	}
}
