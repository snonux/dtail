package fs

import (
	"testing"

	"github.com/mimecast/dtail/internal/regex"
	"github.com/mimecast/dtail/internal/testutil"
)

func TestGrepProcessorBasic(t *testing.T) {
	re, err := regex.New("test", regex.Default)
	testutil.AssertNoError(t, err)

	// Use plain mode to avoid color formatting issues in tests
	gp := NewGrepProcessor(re, true, false, "testhost", 0, 0, 0)

	tests := []struct {
		name        string
		line        string
		shouldMatch bool
	}{
		{"matching line", "this is a test line", true},
		{"non-matching line", "this is another line", false},
		{"empty line", "", false},
		{"exact match", "test", true},
		{"case sensitive", "TEST", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, shouldSend := gp.ProcessLine([]byte(tt.line), 1, "test.log", nil, "test-id")
			
			if shouldSend != tt.shouldMatch {
				t.Errorf("expected shouldSend=%v for line %q", tt.shouldMatch, tt.line)
			}
			
			if shouldSend && len(result) == 0 {
				t.Error("expected non-empty result for matching line")
			}
		})
	}
}

func TestGrepProcessorWithContext(t *testing.T) {
	re, err := regex.New("MATCH", regex.Default)
	testutil.AssertNoError(t, err)

	// Test with before context = 2, after context = 1
	gp := NewGrepProcessor(re, true, false, "testhost", 2, 1, 0)

	lines := []string{
		"line 1",
		"line 2",
		"line 3 MATCH",
		"line 4",
		"line 5",
	}

	var results []string
	
	for i, line := range lines {
		result, shouldSend := gp.ProcessLine([]byte(line), i+1, "test.log", nil, "test-id")
		if shouldSend {
			results = append(results, string(result))
		}
	}

	// The grep processor returns all context lines in one result
	// So we should get one result when the match is found
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First result should contain the before context and the match
	testutil.AssertContains(t, results[0], "line 2")
	testutil.AssertContains(t, results[0], "line 3 MATCH")
	// Second result is the after context
	testutil.AssertContains(t, results[1], "line 4")
}

func TestGrepProcessorMaxCount(t *testing.T) {
	re, err := regex.New("match", regex.Default)
	testutil.AssertNoError(t, err)

	// Limit to 2 matches
	gp := NewGrepProcessor(re, true, false, "testhost", 0, 0, 2)

	matchCount := 0
	for i := 0; i < 5; i++ {
		line := "this is a match line"
		result, shouldSend := gp.ProcessLine([]byte(line), i+1, "test.log", nil, "test-id")
		if shouldSend {
			matchCount++
			if len(result) == 0 {
				t.Error("expected non-empty result")
			}
		}
	}

	if matchCount != 2 {
		t.Errorf("expected exactly 2 matches, got %d", matchCount)
	}
}

func TestGrepProcessorPlainMode(t *testing.T) {
	re, err := regex.New("test", regex.Default)
	testutil.AssertNoError(t, err)

	gp := NewGrepProcessor(re, true, false, "testhost", 0, 0, 0)

	// Test that plain mode preserves line endings
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{"LF ending", []byte("test line\n"), "test line\n"},
		{"CRLF ending", []byte("test line\r\n"), "test line\r\n"},
		{"no ending", []byte("test line"), "test line\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, shouldSend := gp.ProcessLine(tt.input, 1, "test.log", nil, "test-id")
			if !shouldSend {
				t.Fatal("expected line to match")
			}
			if string(result) != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, string(result))
			}
		})
	}
}

func TestGrepProcessorFormatLine(t *testing.T) {
	re, err := regex.New(".", regex.Default)
	testutil.AssertNoError(t, err)

	// Test plain mode formatting to avoid color issues
	gp := NewGrepProcessor(re, true, false, "testhost", 0, 0, 0)
	
	stats := &stats{}
	stats.updatePosition()
	stats.updateLineMatched()
	stats.updateLineTransmitted()
	
	result := gp.formatLine([]byte("test line"), 1, "test.log", stats, "test-id")
	
	// In plain mode, should just get the line with newline
	resultStr := string(result)
	testutil.AssertEqual(t, "test line\n", resultStr)
}