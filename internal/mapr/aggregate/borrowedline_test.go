package aggregate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/protocol"
)

// TestBorrowedLineAliasesTheBuffer is the negative control for every
// retention test in this package: it asserts that a borrowed line really does
// follow later writes to its buffer. If borrowedLine ever started copying,
// this test would fail and the retention tests below would silently stop
// proving anything.
func TestBorrowedLineAliasesTheBuffer(t *testing.T) {
	buffer := pool.BytesBuffer.Get().(*bytes.Buffer)
	defer pool.RecycleBytesBuffer(buffer)

	buffer.Reset()
	buffer.WriteString("  hello  ")
	borrowed := borrowedLine(buffer)
	if borrowed != "hello" {
		t.Fatalf("borrowedLine() = %q, want %q", borrowed, "hello")
	}

	buffer.Reset()
	buffer.WriteString("  world  ")
	if borrowed != "world" {
		t.Fatalf("borrowedLine() = %q after the buffer was reused, want %q; "+
			"the retention tests rely on this aliasing to detect retained views",
			borrowed, "world")
	}
}

func TestBorrowedLineEdgeCases(t *testing.T) {
	if got := borrowedLine(nil); got != "<nil>" {
		t.Errorf("borrowedLine(nil) = %q, want %q", got, "<nil>")
	}

	buffer := pool.BytesBuffer.Get().(*bytes.Buffer)
	defer pool.RecycleBytesBuffer(buffer)
	buffer.Reset()
	if got := borrowedLine(buffer); got != "" {
		t.Errorf("borrowedLine(empty) = %q, want an empty string", got)
	}
	buffer.WriteString(" \t\n ")
	if got := borrowedLine(buffer); got != "" {
		t.Errorf("borrowedLine(whitespace) = %q, want an empty string", got)
	}
}

func TestBuildGroupKeyReusesBuffer(t *testing.T) {
	fields := map[string]string{"host": "alpha", "color": "orange"}
	combinator := protocol.AggregateGroupKeyCombinator

	key := buildGroupKey(make([]byte, 0, 8), []string{"host", "color"}, fields)
	if string(key) != "alpha"+combinator+"orange" {
		t.Fatalf("buildGroupKey() = %q", key)
	}

	// The aggregator reuses the same buffer for the next line.
	fields["host"] = "beta"
	key = buildGroupKey(key[:0], []string{"host"}, fields)
	if string(key) != "beta" {
		t.Fatalf("buildGroupKey() = %q on the reused buffer, want %q", key, "beta")
	}

	key = buildGroupKey(key[:0], nil, fields)
	if len(key) != 0 {
		t.Errorf("buildGroupKey() = %q without a group by clause, want an empty key", key)
	}
}

func TestRecycleLineScratchDropsBorrowedViews(t *testing.T) {
	scratch := lineScratchPool.Get().(*lineScratch)
	scratch.fields["borrowed"] = "view"
	scratch.key = append(scratch.key[:0], "borrowed"...)

	recycleLineScratch(scratch)

	if len(scratch.fields) != 0 {
		t.Errorf("recycleLineScratch() left %#v in the fields map", scratch.fields)
	}
	if len(scratch.key) != 0 {
		t.Errorf("recycleLineScratch() left %q in the key buffer", scratch.key)
	}
	if strings.Contains(string(scratch.key[:cap(scratch.key)]), "\x00\x00") {
		// Only here to keep the key buffer referenced; contents are irrelevant.
		t.Log("key buffer retained its capacity")
	}
}
