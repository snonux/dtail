package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/protocol"
)

// probeTestFormat returns a format of one table with fields fields, each
// with values sample values of valueBytes bytes.
func probeTestFormat(table string, fields, values, valueBytes int) protocol.ProbeFormat {
	tableFields := make(map[string][]string, fields)
	for i := range fields {
		var samples []string
		for j := range values {
			samples = append(samples, fmt.Sprintf("%d-%s", j, strings.Repeat("v", valueBytes)))
		}
		tableFields[fmt.Sprintf("field%03d", i)] = samples
	}
	return protocol.ProbeFormat{
		Tables:    map[string]map[string][]string{table: tableFields},
		Variables: map[string][]string{"$hostname": {"host"}},
		Note:      "note",
	}
}

func decodeTestAnswer(t *testing.T, data []byte, maxBytes int) protocol.ProbeAnswer {
	t.Helper()
	if len(data) > maxBytes {
		t.Fatalf("answer of %d bytes exceeds %d", len(data), maxBytes)
	}
	var answer protocol.ProbeAnswer
	if err := json.Unmarshal(data, &answer); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	return answer
}

func TestMarshalProbeAnswerKeepsAnAnswerThatFits(t *testing.T) {
	answer := protocol.ProbeAnswer{ID: "a", Formats: map[string]protocol.ProbeFormat{
		"default": probeTestFormat("STATS", 3, 2, 10),
	}}
	data, err := marshalProbeAnswer(answer, protocol.ProbeMaxAnswerBytes)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeTestAnswer(t, data, protocol.ProbeMaxAnswerBytes)
	if got.Truncated || len(got.Formats["default"].Tables["STATS"]["field000"]) != 2 ||
		got.Formats["default"].Note != "note" {
		t.Fatalf("answer changed: %s", data)
	}
}

// TestMarshalProbeAnswerDropsSampleValuesFirst checks that an answer too
// large with its sample values keeps every field without them.
func TestMarshalProbeAnswerDropsSampleValuesFirst(t *testing.T) {
	const maxBytes = 4096
	answer := protocol.ProbeAnswer{ID: "a", Formats: map[string]protocol.ProbeFormat{
		"default": probeTestFormat("STATS", 50, 5, 60),
	}}
	data, err := marshalProbeAnswer(answer, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeTestAnswer(t, data, maxBytes)
	fields := got.Formats["default"].Tables["STATS"]
	if !got.Truncated || len(fields) != 50 {
		t.Fatalf("truncated = %v, %d fields, want all 50: %s", got.Truncated, len(fields), data)
	}
	for name, values := range fields {
		if len(values) != 0 {
			t.Fatalf("field %s kept values %v", name, values)
		}
	}
}

// TestMarshalProbeAnswerDropsFieldsLast checks that an answer too large
// without sample values keeps as many items as fit, those of the default
// log format first, and its errors.
func TestMarshalProbeAnswerDropsFieldsLast(t *testing.T) {
	const maxBytes = 4096
	answer := protocol.ProbeAnswer{
		ID:               "a",
		DefaultLogFormat: "zdefault",
		Formats: map[string]protocol.ProbeFormat{
			"aaa":      probeTestFormat("T", 400, 1, 1),
			"zdefault": probeTestFormat("STATS", 100, 1, 1),
		},
		Errors: []protocol.ProbeError{{File: "x", Error: protocol.CommandFailureNoFile}},
	}
	data, err := marshalProbeAnswer(answer, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeTestAnswer(t, data, maxBytes)
	if !got.Truncated || len(got.Errors) != 1 {
		t.Fatalf("truncated = %v, errors = %v", got.Truncated, got.Errors)
	}
	if fields := got.Formats["zdefault"].Tables["STATS"]; len(fields) != 100 {
		t.Fatalf("default log format kept %d of 100 fields", len(fields))
	}
	kept := len(got.Formats["aaa"].Tables["T"])
	if kept == 0 || kept == 400 {
		t.Fatalf("other format kept %d of 400 fields, want some but not all", kept)
	}
	// The cut is as large as fits: one more item would not.
	items := probeItems(withoutSampleValues(answer.Formats), answer.DefaultLogFormat)
	answer.Truncated = true
	answer.Formats = keepProbeItems(items, countProbeItems(got.Formats)+1)
	if more, _ := json.Marshal(answer); len(more) <= maxBytes {
		t.Fatalf("an answer with one more item (%d bytes) fits too", len(more))
	}
}

func countProbeItems(formats map[string]protocol.ProbeFormat) int {
	return len(probeItems(formats, ""))
}
