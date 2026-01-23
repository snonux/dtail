package handlers

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/protocol"
)

// TestDirectTurboWriter_ServerlessPlain tests plain serverless mode output
func TestDirectTurboWriter_ServerlessPlain(t *testing.T) {
	var buf bytes.Buffer
	w := NewDirectTurboWriter(&buf, "testhost", true, true)

	// Write a line without trailing newline
	err := w.WriteLineData([]byte("test line"), 1, "source.log")
	if err != nil {
		t.Fatalf("WriteLineData failed: %v", err)
	}

	// Flush to get output
	err = w.Flush()
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// In plain serverless mode, output should be just the content with newline
	expected := "test line\n"
	if buf.String() != expected {
		t.Errorf("Expected %q, got %q", expected, buf.String())
	}

	// Check stats
	lines, bytesWritten := w.Stats()
	if lines != 1 {
		t.Errorf("Expected 1 line written, got %d", lines)
	}
	if bytesWritten == 0 {
		t.Error("Expected non-zero bytes written")
	}
}

// TestDirectTurboWriter_ServerlessPlainWithNewline tests that existing newlines are preserved
func TestDirectTurboWriter_ServerlessPlainWithNewline(t *testing.T) {
	var buf bytes.Buffer
	w := NewDirectTurboWriter(&buf, "testhost", true, true)

	// Write a line with trailing newline
	err := w.WriteLineData([]byte("test line\n"), 1, "source.log")
	if err != nil {
		t.Fatalf("WriteLineData failed: %v", err)
	}

	err = w.Flush()
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Should not add extra newline
	expected := "test line\n"
	if buf.String() != expected {
		t.Errorf("Expected %q, got %q", expected, buf.String())
	}
}

// TestDirectTurboWriter_ServerlessColored tests colored serverless mode output
// Note: Skipped because it requires color config initialization which is complex to set up in tests
func TestDirectTurboWriter_ServerlessColored(t *testing.T) {
	t.Skip("Requires color config initialization - tested via integration tests")
}

// TestDirectTurboWriter_NetworkPlain tests plain network mode output
func TestDirectTurboWriter_NetworkPlain(t *testing.T) {
	var buf bytes.Buffer
	w := NewDirectTurboWriter(&buf, "testhost", true, false)

	err := w.WriteLineData([]byte("test line"), 1, "source.log")
	if err != nil {
		t.Fatalf("WriteLineData failed: %v", err)
	}

	err = w.Flush()
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// In plain network mode, output should be just the content with newline
	expected := "test line\n"
	if buf.String() != expected {
		t.Errorf("Expected %q, got %q", expected, buf.String())
	}
}

// TestDirectTurboWriter_NetworkFormatted tests formatted network mode output
func TestDirectTurboWriter_NetworkFormatted(t *testing.T) {
	var buf bytes.Buffer
	w := NewDirectTurboWriter(&buf, "testhost", false, false)

	err := w.WriteLineData([]byte("test line"), 99, "myfile.log")
	if err != nil {
		t.Fatalf("WriteLineData failed: %v", err)
	}

	err = w.Flush()
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	output := buf.String()

	// In formatted network mode, output should have protocol structure
	if !strings.HasPrefix(output, "REMOTE") {
		t.Errorf("Expected output to start with REMOTE, got %q", output)
	}
	if !strings.Contains(output, "testhost") {
		t.Errorf("Expected output to contain hostname, got %q", output)
	}
	if !strings.Contains(output, "99") {
		t.Errorf("Expected output to contain line number 99, got %q", output)
	}
	if !strings.Contains(output, "myfile.log") {
		t.Errorf("Expected output to contain source ID, got %q", output)
	}
	// Should end with message delimiter
	if output[len(output)-1] != protocol.MessageDelimiter {
		t.Errorf("Expected output to end with message delimiter, got %q", output)
	}
}

// TestDirectTurboWriter_WriteServerMessage tests server message writing
func TestDirectTurboWriter_WriteServerMessage(t *testing.T) {
	var buf bytes.Buffer
	w := NewDirectTurboWriter(&buf, "testhost", false, false)

	err := w.WriteServerMessage("Hello from server")
	if err != nil {
		t.Fatalf("WriteServerMessage failed: %v", err)
	}

	output := buf.String()

	if !strings.HasPrefix(output, "SERVER") {
		t.Errorf("Expected output to start with SERVER, got %q", output)
	}
	if !strings.Contains(output, "testhost") {
		t.Errorf("Expected output to contain hostname, got %q", output)
	}
	if !strings.Contains(output, "Hello from server") {
		t.Errorf("Expected output to contain message, got %q", output)
	}
}

// TestDirectTurboWriter_WriteServerMessage_Serverless tests that server messages are skipped in serverless mode
func TestDirectTurboWriter_WriteServerMessage_Serverless(t *testing.T) {
	var buf bytes.Buffer
	w := NewDirectTurboWriter(&buf, "testhost", false, true)

	err := w.WriteServerMessage("Hello from server")
	if err != nil {
		t.Fatalf("WriteServerMessage failed: %v", err)
	}

	// In serverless mode, server messages should be skipped
	if buf.Len() != 0 {
		t.Errorf("Expected no output in serverless mode, got %q", buf.String())
	}
}

// TestDirectTurboWriter_WriteServerMessage_HiddenMessage tests hidden message handling
func TestDirectTurboWriter_WriteServerMessage_HiddenMessage(t *testing.T) {
	var buf bytes.Buffer
	w := NewDirectTurboWriter(&buf, "testhost", false, false)

	err := w.WriteServerMessage(".hidden")
	if err != nil {
		t.Fatalf("WriteServerMessage failed: %v", err)
	}

	output := buf.String()

	// Hidden messages (starting with .) should be written directly
	if !strings.HasPrefix(output, ".hidden") {
		t.Errorf("Expected output to start with .hidden, got %q", output)
	}
	// Should NOT have SERVER prefix
	if strings.Contains(output, "SERVER") {
		t.Errorf("Hidden message should not have SERVER prefix, got %q", output)
	}
}

// TestDirectTurboWriter_MultipleLines tests writing multiple lines
func TestDirectTurboWriter_MultipleLines(t *testing.T) {
	var buf bytes.Buffer
	w := NewDirectTurboWriter(&buf, "testhost", true, true)

	for i := uint64(1); i <= 5; i++ {
		err := w.WriteLineData([]byte("line content"), i, "source.log")
		if err != nil {
			t.Fatalf("WriteLineData failed on line %d: %v", i, err)
		}
	}

	err := w.Flush()
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	lines, _ := w.Stats()
	if lines != 5 {
		t.Errorf("Expected 5 lines written, got %d", lines)
	}

	// Count actual lines in output
	outputLines := strings.Count(buf.String(), "\n")
	if outputLines != 5 {
		t.Errorf("Expected 5 lines in output, got %d", outputLines)
	}
}

// TestTurboChannelWriter_WriteLineData tests channel writer line data
func TestTurboChannelWriter_WriteLineData(t *testing.T) {
	ch := make(chan []byte, 10)
	w := NewTurboChannelWriter(ch, "testhost", false, false)

	err := w.WriteLineData([]byte("test line"), 1, "source.log")
	if err != nil {
		t.Fatalf("WriteLineData failed: %v", err)
	}

	// Check that data was sent to channel
	select {
	case data := <-ch:
		output := string(data)
		if !strings.Contains(output, "REMOTE") {
			t.Errorf("Expected output to contain REMOTE, got %q", output)
		}
		if !strings.Contains(output, "test line") {
			t.Errorf("Expected output to contain line content, got %q", output)
		}
	default:
		t.Error("Expected data in channel, got none")
	}
}

// TestTurboChannelWriter_ChannelFull tests behavior when channel is full
func TestTurboChannelWriter_ChannelFull(t *testing.T) {
	ch := make(chan []byte, 1)
	w := NewTurboChannelWriter(ch, "testhost", true, false)

	// Fill the channel
	err := w.WriteLineData([]byte("first"), 1, "source.log")
	if err != nil {
		t.Fatalf("First WriteLineData failed: %v", err)
	}

	// Next write should fail (channel full)
	err = w.WriteLineData([]byte("second"), 2, "source.log")
	if err == nil {
		t.Error("Expected error when channel is full")
	}
	if !strings.Contains(err.Error(), "channel full") {
		t.Errorf("Expected 'channel full' error, got %v", err)
	}
}

// TestTurboChannelWriter_PlainServerless tests plain serverless mode
func TestTurboChannelWriter_PlainServerless(t *testing.T) {
	ch := make(chan []byte, 10)
	w := NewTurboChannelWriter(ch, "testhost", true, true)

	err := w.WriteLineData([]byte("test line"), 1, "source.log")
	if err != nil {
		t.Fatalf("WriteLineData failed: %v", err)
	}

	select {
	case data := <-ch:
		output := string(data)
		// In plain serverless mode, should NOT have REMOTE prefix
		if strings.Contains(output, "REMOTE") {
			t.Errorf("Plain serverless should not have REMOTE prefix, got %q", output)
		}
		if !strings.Contains(output, "test line") {
			t.Errorf("Expected output to contain line content, got %q", output)
		}
	default:
		t.Error("Expected data in channel, got none")
	}
}

// TestTurboChannelWriter_WriteServerMessage tests server message handling
func TestTurboChannelWriter_WriteServerMessage(t *testing.T) {
	ch := make(chan []byte, 10)
	w := NewTurboChannelWriter(ch, "testhost", false, false)

	err := w.WriteServerMessage("Server says hello")
	if err != nil {
		t.Fatalf("WriteServerMessage failed: %v", err)
	}

	select {
	case data := <-ch:
		output := string(data)
		if !strings.Contains(output, "SERVER") {
			t.Errorf("Expected output to contain SERVER, got %q", output)
		}
		if !strings.Contains(output, "Server says hello") {
			t.Errorf("Expected output to contain message, got %q", output)
		}
	default:
		t.Error("Expected data in channel, got none")
	}
}

// TestTurboChannelWriter_WriteServerMessage_Serverless tests server messages skipped in serverless
func TestTurboChannelWriter_WriteServerMessage_Serverless(t *testing.T) {
	ch := make(chan []byte, 10)
	w := NewTurboChannelWriter(ch, "testhost", false, true)

	err := w.WriteServerMessage("Server says hello")
	if err != nil {
		t.Fatalf("WriteServerMessage failed: %v", err)
	}

	// Channel should be empty in serverless mode
	select {
	case <-ch:
		t.Error("Expected no data in channel for serverless mode")
	default:
		// Expected
	}
}

// TestTurboChannelWriter_Stats tests statistics tracking
func TestTurboChannelWriter_Stats(t *testing.T) {
	ch := make(chan []byte, 10)
	w := NewTurboChannelWriter(ch, "testhost", true, true)

	for i := uint64(1); i <= 3; i++ {
		err := w.WriteLineData([]byte("line"), i, "source.log")
		if err != nil {
			t.Fatalf("WriteLineData failed: %v", err)
		}
	}

	lines, bytesWritten := w.Stats()
	if lines != 3 {
		t.Errorf("Expected 3 lines, got %d", lines)
	}
	if bytesWritten == 0 {
		t.Error("Expected non-zero bytes written")
	}
}

// TestDirectLineProcessor tests the line processor wrapper
// Note: Skipped because DirectLineProcessor uses dlog.Server which requires initialization
func TestDirectLineProcessor(t *testing.T) {
	t.Skip("Requires dlog initialization - tested via integration tests")
}

// TestDirectLineProcessor_Close tests the close method
// Note: Skipped because DirectLineProcessor uses dlog.Server which requires initialization
func TestDirectLineProcessor_Close(t *testing.T) {
	t.Skip("Requires dlog initialization - tested via integration tests")
}
