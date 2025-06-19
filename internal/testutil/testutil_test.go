package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTempFile(t *testing.T) {
	content := "test content\nline 2"
	path := TempFile(t, content)
	
	// Check file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("temp file does not exist: %s", path)
	}
	
	// Check content
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read temp file: %v", err)
	}
	
	if string(actual) != content {
		t.Errorf("content mismatch: expected %q, got %q", content, string(actual))
	}
}

func TestTempDir(t *testing.T) {
	dir := TempDir(t)
	
	// Check directory exists
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		t.Errorf("temp dir does not exist: %s", dir)
	}
	
	if !info.IsDir() {
		t.Errorf("path is not a directory: %s", dir)
	}
}

func TestCreateFileTree(t *testing.T) {
	dir := TempDir(t)
	
	files := map[string]string{
		"file1.txt":           "content 1",
		"subdir/file2.txt":    "content 2",
		"subdir/deep/f3.txt":  "content 3",
	}
	
	CreateFileTree(t, dir, files)
	
	// Verify all files exist with correct content
	for path, expectedContent := range files {
		fullPath := filepath.Join(dir, path)
		actual, err := os.ReadFile(fullPath)
		if err != nil {
			t.Errorf("failed to read %s: %v", path, err)
			continue
		}
		
		if string(actual) != expectedContent {
			t.Errorf("content mismatch for %s: expected %q, got %q", 
				path, expectedContent, string(actual))
		}
	}
}

func TestAssertions(t *testing.T) {
	// Test AssertError with real testing.T
	t.Run("AssertError", func(t *testing.T) {
		// This should pass
		err := fmt.Errorf("file not exist")
		AssertError(t, err, "not exist")
		
		// We can't easily test the failure case without causing the test to fail
	})
	
	// Test AssertNoError with real testing.T
	t.Run("AssertNoError", func(t *testing.T) {
		// This should pass
		AssertNoError(t, nil)
		
		// We can't easily test the failure case without causing the test to fail
	})
	
	// Test AssertEqual with real testing.T
	t.Run("AssertEqual", func(t *testing.T) {
		// This should pass
		AssertEqual(t, 42, 42)
		AssertEqual(t, "hello", "hello")
		
		// We can't easily test the failure case without causing the test to fail
	})
	
	// Test AssertContains with real testing.T
	t.Run("AssertContains", func(t *testing.T) {
		// This should pass
		AssertContains(t, "hello world", "world")
		AssertNotContains(t, "hello world", "xyz")
		
		// We can't easily test the failure case without causing the test to fail
	})
}

func TestGenerateTestData(t *testing.T) {
	data := GenerateTestData(3, 10)
	lines := strings.Split(strings.TrimSpace(data), "\n")
	
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	
	for i, line := range lines {
		expectedPrefix := fmt.Sprintf("%d: ", i+1)
		if !strings.HasPrefix(line, expectedPrefix) {
			t.Errorf("line %d doesn't have expected prefix: %s", i, line)
		}
		
		// Check line length (prefix + x's + newline was stripped)
		if len(line) != len(expectedPrefix)+9 {
			t.Errorf("line %d has incorrect length: %d", i, len(line))
		}
	}
}

func TestGenerateLogLines(t *testing.T) {
	lines := GenerateLogLines(10)
	
	if len(lines) != 10 {
		t.Errorf("expected 10 lines, got %d", len(lines))
	}
	
	for i, line := range lines {
		// Check basic log format
		if !strings.Contains(line, "2024-01-15") {
			t.Errorf("line %d missing date: %s", i, line)
		}
		
		// Check log level
		hasLevel := false
		for _, level := range []string{"INFO", "WARN", "ERROR", "DEBUG"} {
			if strings.Contains(line, "["+level+"]") {
				hasLevel = true
				break
			}
		}
		if !hasLevel {
			t.Errorf("line %d missing log level: %s", i, line)
		}
	}
}

// Test CaptureOutput
func TestCaptureOutput(t *testing.T) {
	output := CaptureOutput(t, func() {
		fmt.Print("test output")
	})
	
	if output != "test output" {
		t.Errorf("expected 'test output', got %q", output)
	}
}