package testutil

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TempFile creates a temporary file with the given content and returns its path.
// The file is automatically cleaned up when the test ends.
func TempFile(t *testing.T, content string) string {
	t.Helper()
	
	tmpfile, err := os.CreateTemp("", "dtail-test-*.txt")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		tmpfile.Close()
		os.Remove(tmpfile.Name())
		t.Fatalf("failed to write to temp file: %v", err)
	}
	
	if err := tmpfile.Close(); err != nil {
		os.Remove(tmpfile.Name())
		t.Fatalf("failed to close temp file: %v", err)
	}
	
	t.Cleanup(func() {
		os.Remove(tmpfile.Name())
	})
	
	return tmpfile.Name()
}

// TempDir creates a temporary directory and returns its path.
// The directory is automatically cleaned up when the test ends.
func TempDir(t *testing.T) string {
	t.Helper()
	
	tmpdir, err := os.MkdirTemp("", "dtail-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	
	t.Cleanup(func() {
		os.RemoveAll(tmpdir)
	})
	
	return tmpdir
}

// CreateFileTree creates a directory structure with files based on the provided map.
// Keys are relative file paths, values are file contents.
func CreateFileTree(t *testing.T, baseDir string, files map[string]string) {
	t.Helper()
	
	for path, content := range files {
		fullPath := filepath.Join(baseDir, path)
		dir := filepath.Dir(fullPath)
		
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create directory %s: %v", dir, err)
		}
		
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write file %s: %v", fullPath, err)
		}
	}
}

// AssertFileContents checks that a file contains the expected content.
func AssertFileContents(t *testing.T, path, expected string) {
	t.Helper()
	
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file %s: %v", path, err)
	}
	
	if string(actual) != expected {
		t.Errorf("file content mismatch:\nexpected: %q\nactual: %q", expected, string(actual))
	}
}

// CaptureOutput captures stdout during the execution of a function.
func CaptureOutput(t *testing.T, f func()) string {
	t.Helper()
	
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w
	
	outCh := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		outCh <- buf.String()
	}()
	
	f()
	
	w.Close()
	os.Stdout = old
	
	return <-outCh
}

// AssertError checks that an error is not nil and contains the expected substring.
func AssertError(t *testing.T, err error, contains string) {
	t.Helper()
	
	if err == nil {
		t.Errorf("expected error containing %q, got nil", contains)
		return
	}
	
	if !strings.Contains(err.Error(), contains) {
		t.Errorf("expected error containing %q, got %q", contains, err.Error())
	}
}

// AssertNoError checks that an error is nil.
func AssertNoError(t *testing.T, err error) {
	t.Helper()
	
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// AssertEqual checks that two values are equal.
func AssertEqual(t *testing.T, expected, actual interface{}) {
	t.Helper()
	
	if expected != actual {
		t.Errorf("expected %v, got %v", expected, actual)
	}
}

// AssertContains checks that a string contains a substring.
func AssertContains(t *testing.T, s, substr string) {
	t.Helper()
	
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}

// AssertNotContains checks that a string does not contain a substring.
func AssertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	
	if strings.Contains(s, substr) {
		t.Errorf("expected %q not to contain %q", s, substr)
	}
}

// GenerateTestData generates test data of the specified size.
func GenerateTestData(lines int, lineLength int) string {
	var builder strings.Builder
	line := strings.Repeat("x", lineLength-1) + "\n"
	
	for i := 0; i < lines; i++ {
		builder.WriteString(fmt.Sprintf("%d: %s", i+1, line))
	}
	
	return builder.String()
}

// GenerateLogLines generates realistic log lines for testing.
func GenerateLogLines(count int) []string {
	levels := []string{"INFO", "WARN", "ERROR", "DEBUG"}
	messages := []string{
		"Server started successfully",
		"Connection established",
		"Processing request",
		"Request completed",
		"Connection closed",
		"Error processing file",
		"Timeout occurred",
		"Retrying operation",
	}
	
	lines := make([]string, count)
	for i := 0; i < count; i++ {
		level := levels[i%len(levels)]
		msg := messages[i%len(messages)]
		lines[i] = fmt.Sprintf("2024-01-15 10:00:%02d [%s] %s", i%60, level, msg)
	}
	
	return lines
}

// TableTest is a generic structure for table-driven tests.
type TableTest[T any] struct {
	Name     string
	Input    T
	Expected interface{}
	WantErr  bool
	ErrMsg   string
}