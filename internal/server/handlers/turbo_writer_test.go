package handlers

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
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

type shortWriter struct {
	maxChunk int
	buf      bytes.Buffer
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := len(p)
	if w.maxChunk > 0 && n > w.maxChunk {
		n = w.maxChunk
	}
	w.buf.Write(p[:n])
	return n, nil
}

func TestDirectTurboWriter_FlushHandlesShortWrites(t *testing.T) {
	writer := &shortWriter{maxChunk: 5}
	w := NewDirectTurboWriter(writer, "testhost", true, true)

	if err := w.WriteLineData([]byte("abcdefghij"), 1, "source.log"); err != nil {
		t.Fatalf("WriteLineData failed: %v", err)
	}

	if err := w.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	if got, want := writer.buf.String(), "abcdefghij\n"; got != want {
		t.Fatalf("expected full output %q, got %q", want, got)
	}
}

type zeroWriter struct{}

func (zeroWriter) Write(p []byte) (int, error) {
	return 0, nil
}

func TestDirectTurboWriter_FlushFailsOnZeroProgress(t *testing.T) {
	w := NewDirectTurboWriter(zeroWriter{}, "testhost", true, true)

	if err := w.WriteLineData([]byte("data"), 1, "source.log"); err != nil {
		t.Fatalf("WriteLineData failed: %v", err)
	}

	if err := w.Flush(); err == nil {
		t.Fatal("expected Flush to fail on zero-progress writes")
	} else if err != io.ErrShortWrite {
		t.Fatalf("expected io.ErrShortWrite, got %v", err)
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

func TestTurboNetworkWriterWriteLineDataStopsOnCancellation(t *testing.T) {
	originalLogger := dlog.Server
	dlog.Server = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Server = originalLogger
	})

	turboLines := make(chan []byte, 1)
	turboLines <- []byte("occupied")

	var activeGeneration atomic.Uint64
	activeGeneration.Store(1)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	writer := &TurboNetworkWriter{
		turboLines:  turboLines,
		plain:       true,
		generation:  1,
		ctx:         ctx,
		sendStateCh: make(chan struct{}),
		activeGeneration: func() uint64 {
			return activeGeneration.Load()
		},
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		done <- writer.WriteLineData([]byte("stale line"), 1, "app.log")
	}()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("WriteLineData returned unexpected error: %v", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("WriteLineData did not stop after cancellation")
	}
}

func waitForTurboNetworkWriterSending(t *testing.T, writer *TurboNetworkWriter, want bool) {
	t.Helper()

	deadline := time.After(250 * time.Millisecond)
	for {
		writer.mutex.Lock()
		got := writer.sending
		writer.mutex.Unlock()

		if got == want {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("timed out waiting for sending=%v", want)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestTurboNetworkWriterFlushWaitsForBufferedDataAndInFlightSend(t *testing.T) {
	originalLogger := dlog.Server
	dlog.Server = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Server = originalLogger
	})

	turboLines := make(chan []byte, 1)
	turboLines <- []byte("occupied")

	var activeGeneration atomic.Uint64
	activeGeneration.Store(1)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	writer := &TurboNetworkWriter{
		turboLines:  turboLines,
		plain:       true,
		generation:  1,
		ctx:         ctx,
		bufSize:     8,
		sendStateCh: make(chan struct{}),
		activeGeneration: func() uint64 {
			return activeGeneration.Load()
		},
	}

	if err := writer.WriteLineData([]byte("first"), 1, "app.log"); err != nil {
		t.Fatalf("first WriteLineData failed: %v", err)
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- writer.WriteLineData([]byte("second"), 2, "app.log")
	}()

	waitForTurboNetworkWriterSending(t, writer, true)

	if err := writer.WriteLineData([]byte("third"), 3, "app.log"); err != nil {
		t.Fatalf("third WriteLineData failed: %v", err)
	}

	flushDone := make(chan error, 1)
	go func() {
		flushDone <- writer.Flush()
	}()

	select {
	case err := <-flushDone:
		t.Fatalf("Flush returned early while buffered data was still blocked: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case got := <-turboLines:
		if string(got) != "occupied" {
			t.Fatalf("expected to drain occupied buffer first, got %q", string(got))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out draining occupied turbo channel entry")
	}

	select {
	case got := <-turboLines:
		generation, payload := decodeGeneratedBytes(got)
		if generation != 1 {
			t.Fatalf("expected generation 1, got %d", generation)
		}
		expected := []byte{'f', 'i', 'r', 's', 't', protocol.MessageDelimiter, 's', 'e', 'c', 'o', 'n', 'd', protocol.MessageDelimiter}
		if !bytes.Equal(payload, expected) {
			t.Fatalf("expected buffered send to contain first and second lines, got %q", string(payload))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for buffered send to reach turbo channel")
	}

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("WriteLineData returned unexpected error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second WriteLineData did not finish after the channel drained")
	}

	select {
	case got := <-turboLines:
		generation, payload := decodeGeneratedBytes(got)
		if generation != 1 {
			t.Fatalf("expected generation 1, got %d", generation)
		}
		expected := []byte{'t', 'h', 'i', 'r', 'd', protocol.MessageDelimiter}
		if !bytes.Equal(payload, expected) {
			t.Fatalf("expected buffered flush to send third line, got %q", string(payload))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for buffered Flush output")
	}

	select {
	case err := <-flushDone:
		if err != nil {
			t.Fatalf("Flush returned unexpected error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Flush did not return after buffered data was drained")
	}
}

func TestTurboNetworkWriterStopsWaitingWhenContextIsCancelled(t *testing.T) {
	originalLogger := dlog.Server
	dlog.Server = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Server = originalLogger
	})

	turboLines := make(chan []byte, 1)
	turboLines <- []byte("occupied")

	var activeGeneration atomic.Uint64
	activeGeneration.Store(1)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	writer := &TurboNetworkWriter{
		turboLines:  turboLines,
		plain:       true,
		generation:  1,
		ctx:         ctx,
		sendStateCh: make(chan struct{}),
		activeGeneration: func() uint64 {
			return activeGeneration.Load()
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- writer.WriteLineData([]byte("stale line"), 1, "app.log")
	}()

	waitForTurboNetworkWriterSending(t, writer, true)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("WriteLineData returned unexpected error: %v", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("WriteLineData did not stop after cancellation")
	}

	select {
	case got := <-turboLines:
		if string(got) != "occupied" {
			t.Fatalf("expected to drain occupied buffer first, got %q", string(got))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out draining occupied turbo channel entry")
	}

	select {
	case got := <-turboLines:
		t.Fatalf("unexpected stale output after cancellation: %q", string(got))
	default:
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
