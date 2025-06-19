package protocol

import (
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/testutil"
)

func TestProtocolConstants(t *testing.T) {
	// Test that protocol version follows expected format
	t.Run("protocol version format", func(t *testing.T) {
		// Should be in format X.Y
		parts := strings.Split(ProtocolCompat, ".")
		if len(parts) != 2 {
			t.Errorf("ProtocolCompat should be in X.Y format, got %q", ProtocolCompat)
		}
	})

	// Test message delimiter uniqueness
	t.Run("delimiter uniqueness", func(t *testing.T) {
		// Note: CSVDelimiter and AggregateGroupKeyCombinator intentionally use the same delimiter
		delimiters := map[string]string{
			"MessageDelimiter":     string(MessageDelimiter),
			"FieldDelimiter":       FieldDelimiter,
			"AggregateKVDelimiter": AggregateKVDelimiter,
			"AggregateDelimiter":   AggregateDelimiter,
		}

		// Check that protocol delimiters are unique (excluding CSV/GroupKey which share ",")
		seen := make(map[string]string)
		for name, d := range delimiters {
			if prevName, exists := seen[d]; exists {
				t.Errorf("Delimiter %q used by both %s and %s", d, prevName, name)
			}
			seen[d] = name
		}

		// Verify CSV and GroupKey combinator are the same (by design)
		testutil.AssertEqual(t, CSVDelimiter, AggregateGroupKeyCombinator)
	})

	// Test that delimiters are not empty
	t.Run("non-empty delimiters", func(t *testing.T) {
		if MessageDelimiter == 0 {
			t.Error("MessageDelimiter should not be zero byte")
		}
		if FieldDelimiter == "" {
			t.Error("FieldDelimiter should not be empty")
		}
		if CSVDelimiter == "" {
			t.Error("CSVDelimiter should not be empty")
		}
		if AggregateKVDelimiter == "" {
			t.Error("AggregateKVDelimiter should not be empty")
		}
		if AggregateDelimiter == "" {
			t.Error("AggregateDelimiter should not be empty")
		}
		if AggregateGroupKeyCombinator == "" {
			t.Error("AggregateGroupKeyCombinator should not be empty")
		}
	})

	// Test expected values (for documentation and regression prevention)
	t.Run("expected values", func(t *testing.T) {
		testutil.AssertEqual(t, byte('¬'), MessageDelimiter)
		testutil.AssertEqual(t, "|", FieldDelimiter)
		testutil.AssertEqual(t, ",", CSVDelimiter)
		testutil.AssertEqual(t, "≔", AggregateKVDelimiter)
		testutil.AssertEqual(t, "∥", AggregateDelimiter)
		testutil.AssertEqual(t, ",", AggregateGroupKeyCombinator)
		testutil.AssertEqual(t, "4.1", ProtocolCompat)
	})

	// Test that special delimiters don't conflict with common characters
	t.Run("delimiter safety", func(t *testing.T) {
		// Common characters that shouldn't be used as delimiters
		commonChars := []string{
			" ", "\n", "\r", "\t", // Whitespace
			"a", "e", "i", "o", "u", // Common letters
			"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", // Digits
			".", ":", ";", "-", "_", "/", "\\", // Common punctuation in logs
		}

		delimiters := []string{
			string(MessageDelimiter),
			FieldDelimiter,
			AggregateKVDelimiter,
			AggregateDelimiter,
		}

		for _, delimiter := range delimiters {
			for _, common := range commonChars {
				if delimiter == common {
					t.Errorf("Delimiter %q conflicts with common character", delimiter)
				}
			}
		}
	})
}

func TestDelimiterUsage(t *testing.T) {
	// Test typical protocol message construction and parsing patterns
	t.Run("message construction", func(t *testing.T) {
		// Simulate building a protocol message
		fields := []string{"HEALTH", "OK", "server1", "100"}
		message := strings.Join(fields, FieldDelimiter)
		
		// Should be able to reconstruct fields
		parsed := strings.Split(message, FieldDelimiter)
		if len(parsed) != len(fields) {
			t.Errorf("Expected %d fields, got %d", len(fields), len(parsed))
		}
		for i, field := range fields {
			testutil.AssertEqual(t, field, parsed[i])
		}
	})

	t.Run("aggregate message construction", func(t *testing.T) {
		// Simulate MapReduce aggregation message
		key := "error"
		value := "42"
		kvPair := key + AggregateKVDelimiter + value
		
		// Should be able to parse key-value
		parts := strings.Split(kvPair, AggregateKVDelimiter)
		if len(parts) != 2 {
			t.Fatalf("Expected 2 parts in KV pair, got %d", len(parts))
		}
		testutil.AssertEqual(t, key, parts[0])
		testutil.AssertEqual(t, value, parts[1])
	})

	t.Run("multiple messages", func(t *testing.T) {
		// Simulate multiple messages in a stream
		messages := []string{"MSG1", "MSG2", "MSG3"}
		
		// Build stream with message delimiter between messages
		var parts []string
		for _, msg := range messages {
			parts = append(parts, msg)
		}
		
		// Join with delimiter
		delimiter := string(MessageDelimiter)
		stream := strings.Join(parts, delimiter)
		
		// Parse messages back
		parsed := strings.Split(stream, delimiter)
		if len(parsed) != len(messages) {
			t.Errorf("Expected %d messages, got %d", len(messages), len(parsed))
		}
		for i, msg := range messages {
			if i < len(parsed) {
				testutil.AssertEqual(t, msg, parsed[i])
			}
		}
	})
}

func TestCSVDelimiter(t *testing.T) {
	// Test CSV parsing scenarios
	t.Run("csv field parsing", func(t *testing.T) {
		csvLine := "field1,field2,field3,field4"
		fields := strings.Split(csvLine, CSVDelimiter)
		
		if len(fields) != 4 {
			t.Errorf("Expected 4 CSV fields, got %d", len(fields))
		}
		
		expected := []string{"field1", "field2", "field3", "field4"}
		for i, field := range expected {
			testutil.AssertEqual(t, field, fields[i])
		}
	})
}

func TestGroupKeyCombinator(t *testing.T) {
	// Test group key combination for MapReduce
	t.Run("combine group keys", func(t *testing.T) {
		keys := []string{"host", "service", "level"}
		combined := strings.Join(keys, AggregateGroupKeyCombinator)
		
		// Should be able to split back
		parsed := strings.Split(combined, AggregateGroupKeyCombinator)
		if len(parsed) != len(keys) {
			t.Errorf("Expected %d keys, got %d", len(keys), len(parsed))
		}
		for i, key := range keys {
			testutil.AssertEqual(t, key, parsed[i])
		}
	})
}