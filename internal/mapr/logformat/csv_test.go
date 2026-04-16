package logformat

import (
	"strings"
	"sync"
	"testing"

	"github.com/mimecast/dtail/internal/protocol"
)

func TestCSVLogFormat(t *testing.T) {
	parser, err := NewParser("csv", nil)
	if err != nil {
		t.Errorf("Unable to create parser: %s", err.Error())
	}

	headers := []string{"name", "last_name", "color", "profession", "employee_number"}
	dataLine1 := []string{"Paul", "Buetow", "Orange", "Site Reliability Engineer", "4242"}
	dataLine2 := []string{"Peter", "Bauer", "Black", "CEO", "1"}

	inputs := []string{
		strings.Join(headers, protocol.CSVDelimiter),
		strings.Join(dataLine1, protocol.CSVDelimiter),
		strings.Join(dataLine2, protocol.CSVDelimiter),
	}

	const sourceID = "file-a"

	// First line is the header!
	if _, err := parser.MakeFields(inputs[0], sourceID); err != ErrIgnoreFields {
		t.Errorf("Unable to parse the CSV header")
	}

	// First data line
	fields, err := parser.MakeFields(inputs[1], sourceID)
	if err != nil {
		t.Errorf("Unable to parse first CSV data line: %s", err.Error())
	}
	if val := fields["name"]; val != "Paul" {
		t.Errorf("Expected 'name' to be 'Paul' but got '%s'", val)
	}
	if val := fields["employee_number"]; val != "4242" {
		t.Errorf("Expected 'employee_number' to be '4242' but got '%s'", val)
	}

	// Second data line
	fields, err = parser.MakeFields(inputs[2], sourceID)
	if err != nil {
		t.Errorf("Unable to parse first CSV data line: %s", err.Error())
	}
	if val := fields["last_name"]; val != "Bauer" {
		t.Errorf("Expected 'last_name' to be 'Bauer' but got '%s'", val)
	}
	if val := fields["color"]; val != "Black" {
		t.Errorf("Expected 'color' to be 'Black' but got '%s'", val)
	}
}

// TestCSVLogFormatMultiFileHeaders reproduces the bug where a single
// csvParser instance (as used by Aggregate/TurboAggregate for every file
// in a mapreduce session) was treating the header row of every file after
// the first as a data row, silently corrupting aggregates.
func TestCSVLogFormatMultiFileHeaders(t *testing.T) {
	parser, err := NewParser("csv", nil)
	if err != nil {
		t.Fatalf("Unable to create parser: %s", err.Error())
	}

	headersA := []string{"name", "value"}
	headersB := []string{"color", "count"}

	fileA := []string{
		strings.Join(headersA, protocol.CSVDelimiter),
		strings.Join([]string{"alpha", "1"}, protocol.CSVDelimiter),
		strings.Join([]string{"beta", "2"}, protocol.CSVDelimiter),
	}
	fileB := []string{
		strings.Join(headersB, protocol.CSVDelimiter),
		strings.Join([]string{"orange", "3"}, protocol.CSVDelimiter),
		strings.Join([]string{"black", "4"}, protocol.CSVDelimiter),
	}

	const sourceA = "file-a"
	const sourceB = "file-b"

	// First line of file A is its header.
	if _, err := parser.MakeFields(fileA[0], sourceA); err != ErrIgnoreFields {
		t.Fatalf("Expected header line of file A to be ignored, got err=%v", err)
	}
	for _, line := range fileA[1:] {
		fields, err := parser.MakeFields(line, sourceA)
		if err != nil {
			t.Fatalf("Unable to parse data line %q of file A: %s", line, err.Error())
		}
		if _, ok := fields["name"]; !ok {
			t.Errorf("Expected file A field 'name' for line %q, got %v", line, fields)
		}
	}

	// First line of file B MUST also be treated as a header, not a data row.
	if _, err := parser.MakeFields(fileB[0], sourceB); err != ErrIgnoreFields {
		t.Fatalf("Expected header line of file B to be ignored (bug: header is being consumed as a data row), got err=%v", err)
	}

	// Data lines of file B must be mapped against file B's headers, not
	// file A's.
	for _, line := range fileB[1:] {
		fields, err := parser.MakeFields(line, sourceB)
		if err != nil {
			t.Fatalf("Unable to parse data line %q of file B: %s", line, err.Error())
		}
		if _, ok := fields["color"]; !ok {
			t.Errorf("Expected file B field 'color' for line %q, got %v", line, fields)
		}
		if _, ok := fields["name"]; ok {
			t.Errorf("File B line %q should not carry file A field 'name'; got %v",
				line, fields)
		}
	}
}

// TestCSVLogFormatConcurrentSources ensures the per-source header store is
// safe for concurrent access across multiple sourceIDs, matching how the
// turbo aggregator drives the parser from batched lines across files.
func TestCSVLogFormatConcurrentSources(t *testing.T) {
	parser, err := NewParser("csv", nil)
	if err != nil {
		t.Fatalf("Unable to create parser: %s", err.Error())
	}

	header := strings.Join([]string{"name", "value"}, protocol.CSVDelimiter)
	data := strings.Join([]string{"alpha", "1"}, protocol.CSVDelimiter)

	const workers = 16
	const iterations = 64

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			sourceID := "source-" + string(rune('a'+id))
			if _, err := parser.MakeFields(header, sourceID); err != ErrIgnoreFields {
				t.Errorf("worker %d: expected header to be ignored, got err=%v", id, err)
				return
			}
			for i := 0; i < iterations; i++ {
				fields, err := parser.MakeFields(data, sourceID)
				if err != nil {
					t.Errorf("worker %d: parse err=%v", id, err)
					return
				}
				if fields["name"] != "alpha" {
					t.Errorf("worker %d: expected name=alpha, got %q", id, fields["name"])
					return
				}
			}
		}(w)
	}
	wg.Wait()
}
