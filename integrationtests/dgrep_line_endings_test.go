package integrationtests

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/config"
)

// TestDGrepLineEndings verifies that dgrep preserves line endings correctly
func TestDGrepLineEndings(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Create a test file with various content
	testContent := `Line 1 with pattern
Line 2 no test
Line 3 with pattern again
Line 4 no test
Line 5 final pattern`
	
	testFile := "test_grep_line_endings.txt"
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	defer os.Remove(testFile)

	t.Run("BasicGrep", func(t *testing.T) {
		// Run dgrep searching for "pattern"
		cmd := exec.Command("../dgrep", "--cfg", "none", "--grep", "pattern", testFile)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dgrep command failed: %v", err)
		}

		// Should get 3 matching lines, each with proper line ending
		outputStr := string(output)
		lines := strings.Split(strings.TrimRight(outputStr, "\n"), "\n")
		if len(lines) != 3 {
			t.Errorf("Expected 3 matching lines, got %d lines. Output:\n%s", 
				len(lines), outputStr)
		}

		// Verify each line contains "pattern"
		for i, line := range lines {
			if !strings.Contains(line, "pattern") {
				t.Errorf("Line %d should contain 'pattern': %s", i, line)
			}
		}
	})

	t.Run("WithContext", func(t *testing.T) {
		// Test with before and after context
		cmd := exec.Command("../dgrep", "--cfg", "none", 
			"--before", "1", "--after", "1", "--grep", "Line 3", testFile)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dgrep command failed: %v", err)
		}

		// Should get Line 2 (before), Line 3 (match), Line 4 (after)
		lines := strings.Split(strings.TrimRight(string(output), "\n"), "\n")
		if len(lines) != 3 {
			t.Errorf("Expected 3 lines with context, got %d. Output:\n%s", 
				len(lines), string(output))
		}

		// Verify the lines
		if !strings.Contains(lines[0], "Line 2") {
			t.Errorf("First line should be before context (Line 2): %s", lines[0])
		}
		if !strings.Contains(lines[1], "Line 3") {
			t.Errorf("Second line should be the match (Line 3): %s", lines[1])
		}
		if !strings.Contains(lines[2], "Line 4") {
			t.Errorf("Third line should be after context (Line 4): %s", lines[2])
		}
	})

	t.Run("NoMatches", func(t *testing.T) {
		// Test with pattern that doesn't match
		cmd := exec.Command("../dgrep", "--cfg", "none", "--grep", "nonexistent", testFile)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dgrep command failed: %v", err)
		}

		// Should get empty output
		if len(output) != 0 {
			t.Errorf("Expected empty output for no matches, got: %q", string(output))
		}
	})

	t.Run("PlainMode", func(t *testing.T) {
		// Test with --plain flag
		cmd := exec.Command("../dgrep", "--plain", "--cfg", "none", "--grep", "pattern", testFile)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dgrep command failed: %v", err)
		}

		// In plain mode, should still have proper line endings
		outputStr := string(output)
		lines := strings.Split(strings.TrimRight(outputStr, "\n"), "\n")
		if len(lines) != 3 {
			t.Errorf("Plain mode: Expected 3 matching lines, got %d lines", 
				len(lines))
		}
	})

	t.Run("MultipleFiles", func(t *testing.T) {
		// Create additional test files
		testFile2 := "test_grep_line_endings2.txt"
		testFile3 := "test_grep_line_endings3.txt"
		
		content2 := "File 2 with pattern\nFile 2 no test\n"
		content3 := "File 3 no test\nFile 3 with pattern\n"
		
		if err := os.WriteFile(testFile2, []byte(content2), 0644); err != nil {
			t.Fatalf("Failed to create test file 2: %v", err)
		}
		defer os.Remove(testFile2)
		
		if err := os.WriteFile(testFile3, []byte(content3), 0644); err != nil {
			t.Fatalf("Failed to create test file 3: %v", err)
		}
		defer os.Remove(testFile3)

		// Run dgrep with multiple files
		cmd := exec.Command("../dgrep", "--cfg", "none", "--grep", "pattern",
			testFile, testFile2, testFile3)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dgrep command failed: %v", err)
		}

		// Should get 5 matching lines total (3 + 1 + 1)
		lines := strings.Split(strings.TrimRight(string(output), "\n"), "\n")
		if len(lines) != 5 {
			t.Errorf("Expected 5 matching lines from multiple files, got %d. Output:\n%s", 
				len(lines), string(output))
		}

		// Verify all lines contain "pattern"
		for i, line := range lines {
			if !strings.Contains(line, "pattern") {
				t.Errorf("Line %d should contain 'pattern': %s", i, line)
			}
		}
	})
}