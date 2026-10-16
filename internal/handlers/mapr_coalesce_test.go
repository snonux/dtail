package handlers

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/mimecast/dtail/internal/protocol"
)

func newMaprCoalesceTestHandler(activeGeneration uint64) *baseHandler {
	return newBaseHandler(context.Background(), baseHandlerConfig{
		serverMessages:   make(chan string, 4),
		maprMessages:     make(chan string, 16),
		hostname:         "testhost",
		activeGeneration: func() uint64 { return activeGeneration },
	})
}

// encodeAggregateFrames returns the raw aggregate frames of contents in order.
func encodeAggregateFrames(contents ...string) []byte {
	var want bytes.Buffer
	for _, content := range contents {
		protocol.RawFraming.EncodeMessage(&want, protocol.Message{
			Kind: protocol.MessageAggregate, Hostname: "testhost", Content: content})
	}
	return want.Bytes()
}

func TestReadCoalescesQueuedMaprMessages(t *testing.T) {
	handler := newMaprCoalesceTestHandler(0)
	var contents []string
	for i := 0; i < 8; i++ {
		content := fmt.Sprintf("group%d≔%d", i, i)
		contents = append(contents, content)
		handler.maprMessages <- content
	}

	p := make([]byte, 4096)
	n, err := handler.Read(p)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if want := encodeAggregateFrames(contents...); !bytes.Equal(p[:n], want) {
		t.Fatalf("one Read = %q, want all queued frames %q", p[:n], want)
	}
	if len(handler.maprMessages) != 0 {
		t.Fatalf("%d mapr messages left queued, want 0", len(handler.maprMessages))
	}
}

func TestReadCoalescingFiltersEachGeneration(t *testing.T) {
	handler := newMaprCoalesceTestHandler(2)
	handler.maprMessages <- encodeGeneratedMessage(2, "fresh1")
	handler.maprMessages <- encodeGeneratedMessage(1, "stale")
	handler.maprMessages <- encodeGeneratedMessage(2, "fresh2")
	handler.maprMessages <- "ungenerated"

	p := make([]byte, 4096)
	n, err := handler.Read(p)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if want := encodeAggregateFrames("fresh1", "fresh2", "ungenerated"); !bytes.Equal(p[:n], want) {
		t.Fatalf("Read = %q, want %q", p[:n], want)
	}
}

// TestReadCoalescingStopsAtReadSize checks that a Read takes no more queued
// mapr messages than fit into its buffer, keeps the rest of the last frame
// for the next Read, and that the stream equals the uncoalesced one.
func TestReadCoalescingStopsAtReadSize(t *testing.T) {
	handler := newMaprCoalesceTestHandler(0)
	contents := []string{"aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc", "dddddddddd", "eeeeeeeeee"}
	for _, content := range contents {
		handler.maprMessages <- content
	}
	want := encodeAggregateFrames(contents...)
	frameLen := len(want) / len(contents)
	bufSize := frameLen*2 + frameLen/2

	p := make([]byte, bufSize)
	n, err := handler.Read(p)
	if err != nil || n != bufSize {
		t.Fatalf("first Read() = %d, %v, want %d, nil", n, err, bufSize)
	}
	if left := len(handler.maprMessages); left != 2 {
		t.Fatalf("%d mapr messages left queued after the first Read, want 2", left)
	}
	got := append([]byte(nil), p[:n]...)
	got = append(got, readExactly(t, handler, bufSize, len(want)-n)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("coalesced stream = %q, want %q", got, want)
	}
}

func TestTryReadQueuedMaprDefersToServerMessage(t *testing.T) {
	handler := newMaprCoalesceTestHandler(0)
	handler.ensureFlushChannels()
	handler.maprMessages <- "group"
	handler.serverMessages <- "server\n"
	if message, ok := handler.tryReadQueuedMapr(); ok {
		t.Fatalf("tryReadQueuedMapr() = %q, true with a server message queued", message)
	}
	<-handler.serverMessages
	handler.flushErrors <- "flush error\n"
	if message, ok := handler.tryReadQueuedMapr(); ok {
		t.Fatalf("tryReadQueuedMapr() = %q, true with a flush error queued", message)
	}
	<-handler.flushErrors
	if message, ok := handler.tryReadQueuedMapr(); !ok || message != "group" {
		t.Fatalf("tryReadQueuedMapr() = %q, %v, want group, true", message, ok)
	}
}

func TestTryReadQueuedMaprDefersToPayload(t *testing.T) {
	handler := newMaprCoalesceTestHandler(0)
	handler.EnableDirectOutput()
	mustEnqueueOutput(t, &handler.output, 0, []byte("payload\n"))
	handler.maprMessages <- "group"
	if message, ok := handler.tryReadQueuedMapr(); ok {
		t.Fatalf("tryReadQueuedMapr() = %q, true with payload buffered", message)
	}
	if len(handler.maprMessages) != 1 {
		t.Fatal("tryReadQueuedMapr() took the mapr message although payload was buffered")
	}
}
