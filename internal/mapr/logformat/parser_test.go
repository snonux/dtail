package logformat

import (
	"strings"
	"testing"
)

type testParser struct{}

func (p *testParser) MakeFields(maprLine string) (map[string]string, error) {
	return map[string]string{"line": maprLine}, nil
}

func TestRegisterParserValidation(t *testing.T) {
	if err := RegisterParser("", wrapParserFactory(newDefaultParser)); err == nil {
		t.Errorf("Expected error when registering parser with empty name")
	}

	if err := RegisterParser("test-nil-factory", nil); err == nil {
		t.Errorf("Expected error when registering parser with nil factory")
	}
}

func TestNewParserUsesRegistry(t *testing.T) {
	const parserName = "unit-test-registry-parser"

	if err := RegisterParser(parserName, func(string, string, int) (Parser, error) {
		return &testParser{}, nil
	}); err != nil {
		t.Fatalf("Unable to register parser: %s", err.Error())
	}

	parser, err := NewParser(parserName, nil)
	if err != nil {
		t.Fatalf("Unable to create parser from registry: %s", err.Error())
	}

	fields, err := parser.MakeFields("hello")
	if err != nil {
		t.Fatalf("Unable to parse line: %s", err.Error())
	}
	if fields["line"] != "hello" {
		t.Errorf("Expected custom parser output, got '%s'", fields["line"])
	}
}

func TestNewParserFallbackToDefault(t *testing.T) {
	parser, err := NewParser("missing-parser-format", nil)
	if err == nil {
		t.Fatalf("Expected NewParser to return error for missing parser format")
	}
	if !strings.Contains(err.Error(), "No 'missing-parser-format' mapr log format") {
		t.Errorf("Unexpected error message: %s", err.Error())
	}
	if parser == nil {
		t.Fatalf("Expected default parser fallback when format is missing")
	}

	fields, parseErr := parser.MakeFields(
		"INFO|20211002-072342|1|parser_test.go:0|8|14|7|0.21|471h0m21s|MAPREDUCE:STATS|foo=bar",
	)
	if parseErr != nil {
		t.Fatalf("Fallback parser failed to parse line: %s", parseErr.Error())
	}
	if val, ok := fields["$severity"]; !ok || val != "INFO" {
		t.Errorf("Fallback parser did not behave like default parser")
	}
}
