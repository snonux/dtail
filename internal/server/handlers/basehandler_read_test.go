package handlers

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/protocol"
)

// newReadTestHandler returns a baseHandler suitable for exercising Read
// directly, without any output or generation scoping involved.
func newReadTestHandler() baseHandler {
	return baseHandler{
		done:           internal.NewDone(),
		lines:          make(chan *line.Line, 4),
		serverMessages: make(chan string, 4),
		maprMessages:   make(chan string, 4),
		hostname:       "testhost",
	}
}

// readExactly drains wantLen bytes from the handler using a buffer of bufSize
// bytes per Read call, failing the test when Read errors or stalls. Stalls are
// detected fast via consecutive empty reads and a wall-clock deadline: an
// empty Read means the handler blocked ~1s in its poll select without data,
// so two in a row indicate the remaining message bytes were dropped (e.g. a
// regression back to resetting readBuf) and we fail with a diagnostic instead
// of running into the go test timeout.
func readExactly(t *testing.T, handler *baseHandler, bufSize, wantLen int) []byte {
	t.Helper()

	var got []byte
	p := make([]byte, bufSize)
	deadline := time.Now().Add(10 * time.Second)
	emptyReads := 0
	for len(got) < wantLen {
		if time.Now().After(deadline) {
			t.Fatalf("Read stalled: got %d of %d bytes before deadline",
				len(got), wantLen)
		}
		n, err := handler.Read(p)
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if n == 0 {
			emptyReads++
			if emptyReads >= 2 {
				t.Fatalf("Read stalled: got %d of %d bytes, %d consecutive empty reads",
					len(got), wantLen, emptyReads)
			}
			continue
		}
		emptyReads = 0
		got = append(got, p[:n]...)
	}
	return got
}

// expectedRemoteLine renders the protocol message Read is expected to emit
// for a line delivered via the lines channel in non-plain mode.
func expectedRemoteLine(content []byte, count uint64, sourceID string) []byte {
	var want bytes.Buffer
	formatRemoteLine(&want, "testhost", fmt.Sprintf("%3d", 100), count,
		sourceID, content)
	return want.Bytes()
}

// TestBaseHandlerReadLargeLineAcrossMultipleReads reproduces the original
// bug: a line larger than the caller's buffer must arrive completely,
// including the trailing message delimiter, across multiple Read calls.
func TestBaseHandlerReadLargeLineAcrossMultipleReads(t *testing.T) {
	handler := newReadTestHandler()

	content := bytes.Repeat([]byte("x"), 1000)
	handler.lines <- line.New(bytes.NewBuffer(append([]byte{}, content...)),
		1, 100, "test.log")

	want := expectedRemoteLine(content, 1, "test.log")
	got := readExactly(t, &handler, 32, len(want))

	if !bytes.Equal(got, want) {
		t.Fatalf("large line corrupted across reads:\ngot  %q\nwant %q", got, want)
	}
	if got[len(got)-1] != protocol.MessageDelimiter {
		t.Fatalf("message delimiter lost, last byte = %q", got[len(got)-1])
	}
}

// TestBaseHandlerReadLargeServerMessageAcrossMultipleReads verifies the
// serverMessages path keeps its remainder across Read calls as well.
func TestBaseHandlerReadLargeServerMessageAcrossMultipleReads(t *testing.T) {
	handler := newReadTestHandler()

	message := "server says: " + string(bytes.Repeat([]byte("y"), 500))
	handler.serverMessages <- message

	var want bytes.Buffer
	formatServerMessage(&want, "testhost", message, false)
	got := readExactly(t, &handler, 32, want.Len())

	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("large server message corrupted across reads:\ngot  %q\nwant %q",
			got, want.Bytes())
	}
}

// TestBaseHandlerReadLargeMaprMessageAcrossMultipleReads verifies the
// maprMessages AGGREGATE path keeps its remainder across Read calls.
func TestBaseHandlerReadLargeMaprMessageAcrossMultipleReads(t *testing.T) {
	handler := newReadTestHandler()

	message := "aggregated " + string(bytes.Repeat([]byte("m"), 500))
	handler.maprMessages <- message

	var want bytes.Buffer
	want.WriteString("AGGREGATE")
	want.WriteString(protocol.FieldDelimiter)
	want.WriteString("testhost")
	want.WriteString(protocol.FieldDelimiter)
	want.WriteString(message)
	want.WriteByte(protocol.MessageDelimiter)

	got := readExactly(t, &handler, 32, want.Len())
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("large mapr message corrupted across reads:\ngot  %q\nwant %q",
			got, want.Bytes())
	}
}

// TestBaseHandlerReadLargeHiddenMessageAcrossMultipleReads verifies the
// hidden-message path (messages starting with '.') keeps its remainder across
// Read calls: hidden messages are forwarded verbatim plus the delimiter.
func TestBaseHandlerReadLargeHiddenMessageAcrossMultipleReads(t *testing.T) {
	handler := newReadTestHandler()

	message := ".hidden " + string(bytes.Repeat([]byte("h"), 500))
	handler.serverMessages <- message

	want := append([]byte(message), protocol.MessageDelimiter)
	got := readExactly(t, &handler, 32, len(want))
	if !bytes.Equal(got, want) {
		t.Fatalf("large hidden message corrupted across reads:\ngot  %q\nwant %q",
			got, want)
	}
}

// TestBaseHandlerReadDrainsRemainderBeforeOutputData verifies that a pending
// readBuf remainder is fully delivered before any output payload when output
// mode is toggled on between Reads: the remainder belongs to a message that
// was already accepted for delivery, so output output must not preempt it.
func TestBaseHandlerReadDrainsRemainderBeforeOutputData(t *testing.T) {
	// The output read path logs via dlog.Server, which is nil in unit tests;
	// stub it out like the other output tests in this package do.
	originalLogger := dlog.Server
	dlog.Server = &dlog.DLog{}
	t.Cleanup(func() { dlog.Server = originalLogger })

	handler := newReadTestHandler()

	message := "regular " + string(bytes.Repeat([]byte("r"), 200))
	handler.serverMessages <- message
	var want bytes.Buffer
	formatServerMessage(&want, "testhost", message, false)

	// First small Read leaves the rest of the message in readBuf.
	p := make([]byte, 16)
	n, err := handler.Read(p)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	got := append([]byte{}, p[:n]...)

	// Toggle output mode on mid-message and queue a output payload.
	if !handler.output.enable() {
		t.Fatal("expected output enable to switch output mode on")
	}
	outputPayload := []byte("output payload data")
	handler.output.channel() <- outputPayload

	total := want.Len() + len(outputPayload)
	got = append(got, readExactly(t, &handler, 16, total-len(got))...)

	if !bytes.Equal(got[:want.Len()], want.Bytes()) {
		t.Fatalf("output data preempted pending remainder:\ngot  %q\nwant %q",
			got[:want.Len()], want.Bytes())
	}
	if !bytes.Equal(got[want.Len():], outputPayload) {
		t.Fatalf("output payload corrupted:\ngot  %q\nwant %q",
			got[want.Len():], outputPayload)
	}
}

// TestBaseHandlerReadDelimiterAloneInFinalRead verifies the boundary where
// the caller's buffer holds everything except the trailing delimiter, so the
// final Read must deliver the delimiter as its sole byte.
func TestBaseHandlerReadDelimiterAloneInFinalRead(t *testing.T) {
	handler := newReadTestHandler()

	message := "delimiter boundary"
	var want bytes.Buffer
	formatServerMessage(&want, "testhost", message, false)
	handler.serverMessages <- message

	p := make([]byte, want.Len()-1)
	n, err := handler.Read(p)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n != want.Len()-1 || !bytes.Equal(p[:n], want.Bytes()[:n]) {
		t.Fatalf("first read mismatch: n = %d, want %d", n, want.Len()-1)
	}

	n, err = handler.Read(p)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n != 1 || p[0] != protocol.MessageDelimiter {
		t.Fatalf("expected lone delimiter in final read, got %q", p[:n])
	}
}

// TestBaseHandlerReadExactFitBuffer verifies that a message exactly filling
// the caller's buffer leaves no stale remainder behind: the next message must
// start fresh instead of being glued to leftover bytes.
func TestBaseHandlerReadExactFitBuffer(t *testing.T) {
	handler := newReadTestHandler()

	first := "exact fit"
	var firstWant bytes.Buffer
	formatServerMessage(&firstWant, "testhost", first, false)

	handler.serverMessages <- first
	p := make([]byte, firstWant.Len())
	n, err := handler.Read(p)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n != firstWant.Len() || !bytes.Equal(p[:n], firstWant.Bytes()) {
		t.Fatalf("exact-fit read mismatch:\ngot  %q\nwant %q", p[:n], firstWant.Bytes())
	}

	second := "next message"
	var secondWant bytes.Buffer
	formatServerMessage(&secondWant, "testhost", second, false)

	handler.serverMessages <- second
	got := readExactly(t, &handler, firstWant.Len(), secondWant.Len())
	if !bytes.Equal(got, secondWant.Bytes()) {
		t.Fatalf("stale remainder leaked into next message:\ngot  %q\nwant %q",
			got, secondWant.Bytes())
	}
}

// TestBaseHandlerReadMultipleQueuedMessages verifies that several queued
// messages, each larger than the read buffer, arrive back to back in order
// and without corruption.
func TestBaseHandlerReadMultipleQueuedMessages(t *testing.T) {
	handler := newReadTestHandler()

	var want bytes.Buffer
	for i := 0; i < 3; i++ {
		content := bytes.Repeat([]byte{byte('a' + i)}, 100)
		handler.lines <- line.New(bytes.NewBuffer(append([]byte{}, content...)),
			uint64(i+1), 100, "queued.log")
		want.Write(expectedRemoteLine(content, uint64(i+1), "queued.log"))
	}

	got := readExactly(t, &handler, 16, want.Len())
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("queued messages corrupted:\ngot  %q\nwant %q", got, want.Bytes())
	}
}

// TestBaseHandlerReadPlainEmptyLine verifies the edge case of an empty line
// in plain mode: the message consists of the delimiter only.
func TestBaseHandlerReadPlainEmptyLine(t *testing.T) {
	handler := newReadTestHandler()
	handler.plain = true

	handler.lines <- line.New(&bytes.Buffer{}, 1, 100, "empty.log")

	p := make([]byte, 8)
	n, err := handler.Read(p)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n != 1 || p[0] != protocol.MessageDelimiter {
		t.Fatalf("expected single delimiter byte, got %q", p[:n])
	}
}
