package integrationtests

import (
	"bytes"
	"os"
	"os/exec"
	"testing"

	"github.com/mimecast/dtail/internal/config"
)

// TestDCatLineEndings verifies that dcat preserves line endings correctly
func TestDCatLineEndings(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Create a test file with various line endings
	testContent := "Line 1\nLine 2\nLine 3 with no ending"
	testFile := "test_line_endings.txt"
	
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	defer os.Remove(testFile)

	t.Run("Serverless", func(t *testing.T) {
		// Run dcat in serverless mode with colors disabled for testing
		cmd := exec.Command("../dcat", "--cfg", "none", "--noColor", testFile)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dcat command failed: %v", err)
		}

		// Expected output should have line endings preserved
		// Note: dcat adds a newline after the last line too
		expected := "Line 1\nLine 2\nLine 3 with no ending\n"
		
		if string(output) != expected {
			t.Errorf("Line endings not preserved correctly.\nExpected:\n%q\nGot:\n%q", 
				expected, string(output))
		}
	})

	t.Run("PlainMode", func(t *testing.T) {
		// Test with --plain flag which should preserve exact file content
		cmd := exec.Command("../dcat", "--plain", "--cfg", "none", testFile)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dcat command failed: %v", err)
		}

		// In plain mode, content should match exactly
		if string(output) != testContent {
			t.Errorf("Plain mode should preserve exact content.\nExpected:\n%q\nGot:\n%q", 
				testContent, string(output))
		}
	})

	t.Run("MultipleFiles", func(t *testing.T) {
		// Create additional test files
		testFile2 := "test_line_endings2.txt"
		testFile3 := "test_line_endings3.txt"
		
		if err := os.WriteFile(testFile2, []byte("File 2 Line 1\nFile 2 Line 2\n"), 0644); err != nil {
			t.Fatalf("Failed to create test file 2: %v", err)
		}
		defer os.Remove(testFile2)
		
		if err := os.WriteFile(testFile3, []byte("File 3 Line 1\n"), 0644); err != nil {
			t.Fatalf("Failed to create test file 3: %v", err)
		}
		defer os.Remove(testFile3)

		// Run dcat with multiple files
		cmd := exec.Command("../dcat", "--cfg", "none", "--noColor", testFile, testFile2, testFile3)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dcat command failed: %v", err)
		}

		// Verify that lines from all files are separated properly
		lines := bytes.Split(output, []byte{'\n'})
		// We expect 7 lines total (3 + 2 + 1 + empty line at end)
		if len(lines) != 7 {
			t.Errorf("Expected 7 lines, got %d. Output:\n%s", len(lines), string(output))
		}
	})

	t.Run("EmptyFile", func(t *testing.T) {
		// Test with empty file
		emptyFile := "empty.txt"
		if err := os.WriteFile(emptyFile, []byte(""), 0644); err != nil {
			t.Fatalf("Failed to create empty file: %v", err)
		}
		defer os.Remove(emptyFile)

		cmd := exec.Command("../dcat", "--cfg", "none", "--noColor", emptyFile)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dcat command failed: %v", err)
		}

		// Empty file should produce empty output
		if len(output) != 0 {
			t.Errorf("Expected empty output for empty file, got: %q", string(output))
		}
	})

	t.Run("CRLFLineEndings", func(t *testing.T) {
		// Test with Windows-style CRLF line endings
		crlfFile := "crlf_test.txt"
		crlfContent := "Line 1\r\nLine 2\r\nLine 3\r\n"
		
		if err := os.WriteFile(crlfFile, []byte(crlfContent), 0644); err != nil {
			t.Fatalf("Failed to create CRLF test file: %v", err)
		}
		defer os.Remove(crlfFile)

		// Run dcat in regular mode
		cmd := exec.Command("../dcat", "--cfg", "none", "--noColor", crlfFile)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("dcat command failed: %v", err)
		}

		// In regular mode, CRLF should be normalized to LF
		expected := "Line 1\nLine 2\nLine 3\n"
		if string(output) != expected {
			t.Errorf("CRLF not handled correctly.\nExpected:\n%q\nGot:\n%q", 
				expected, string(output))
		}

		// In plain mode, CRLF should be preserved
		cmd = exec.Command("../dcat", "--plain", "--cfg", "none", crlfFile)
		output, err = cmd.Output()
		if err != nil {
			t.Fatalf("dcat --plain command failed: %v", err)
		}

		if string(output) != crlfContent {
			t.Errorf("Plain mode should preserve CRLF.\nExpected:\n%q\nGot:\n%q", 
				crlfContent, string(output))
		}
	})
}