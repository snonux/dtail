package handlers

import (
	"bytes"
	"testing"
)

// TestGrepLineProcessor_StatsBytesWrittenMatchesOutput verifies that the
// bytesWritten stat counts each line's formatted bytes exactly once even
// though writeBuf accumulates many lines before flushing. Regression test:
// the stat used to add the whole accumulated buffer length per line, so it
// grew quadratically with the number of buffered lines.
func TestGrepLineProcessor_StatsBytesWrittenMatchesOutput(t *testing.T) {
	tests := []struct {
		name       string
		plain      bool
		serverless bool
	}{
		{name: "plain", plain: true, serverless: false},
		{name: "network formatted", plain: false, serverless: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := NewGrepLineProcessor(&buf, "testhost", tt.plain, tt.serverless)

			// All lines stay buffered (well below the 64KB flush threshold)
			// until Flush, so bytesWritten must equal the flushed output size.
			const numLines = 50
			for i := uint64(1); i <= numLines; i++ {
				line := bytes.NewBufferString("grep match line")
				if err := p.ProcessLine(line, i, "source.log"); err != nil {
					t.Fatalf("ProcessLine failed on line %d: %v", i, err)
				}
			}
			if err := p.Flush(); err != nil {
				t.Fatalf("Flush failed: %v", err)
			}

			lines, bytesWritten := p.Stats()
			if lines != numLines {
				t.Errorf("Expected %d lines processed, got %d", numLines, lines)
			}
			if want := uint64(buf.Len()); bytesWritten != want {
				t.Errorf("Expected bytesWritten %d (flushed output size), got %d", want, bytesWritten)
			}
		})
	}
}

// TestGrepLineProcessor_StatsBytesWrittenAcrossFlushThreshold verifies the
// bytesWritten stat stays exact when the buffer crosses the flush threshold
// several times during processing.
func TestGrepLineProcessor_StatsBytesWrittenAcrossFlushThreshold(t *testing.T) {
	var buf bytes.Buffer
	p := NewGrepLineProcessor(&buf, "testhost", true, false)
	p.bufSize = 32 // Force several intermediate flushes.

	const numLines = 20
	content := "0123456789" // 11 bytes with the message delimiter.
	for i := uint64(1); i <= numLines; i++ {
		if err := p.ProcessLine(bytes.NewBufferString(content), i, "source.log"); err != nil {
			t.Fatalf("ProcessLine failed on line %d: %v", i, err)
		}
	}
	if err := p.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	lines, bytesWritten := p.Stats()
	if lines != numLines {
		t.Errorf("Expected %d lines processed, got %d", numLines, lines)
	}
	want := uint64(numLines * (len(content) + 1))
	if bytesWritten != want {
		t.Errorf("Expected bytesWritten %d, got %d", want, bytesWritten)
	}
	if got := uint64(buf.Len()); got != want {
		t.Errorf("Expected flushed output size %d, got %d", want, got)
	}
}
