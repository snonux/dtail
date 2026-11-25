package handlers

import (
	"testing"

	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
	maprclient "github.com/mimecast/dtail/internal/mapr/client"
	"github.com/mimecast/dtail/internal/protocol"
)

// TestHandlerKeepsLatestProgress feeds progress frames, an invalid one and a
// payload line that only looks like one to a client handler.
func TestHandlerKeepsLatestProgress(t *testing.T) {
	recorder := &payloadRecorder{}
	handler := NewClientHandler("srv1", recorder)
	var _ ProgressReporter = handler
	if _, ok := handler.Progress(); ok {
		t.Fatal("progress before any frame")
	}
	latest := protocol.Progress{Bytes: 20, Total: 30, Files: 1, FilesTotal: 2}
	stream := frame(
		marked(protocol.HiddenCapabilitiesPrefix),
		marked(protocol.EncodeProgress(protocol.Progress{Bytes: 10, Total: 30, FilesTotal: 2})),
		marked(protocol.EncodeProgress(latest)+"\n"),
		marked(protocol.HiddenProgressPrefix+"bytes=x"),
		protocol.EncodeProgress(protocol.Progress{Bytes: 99, Total: 99})+"\n",
	)
	writeInChunks(t, handler, stream, 7)

	got, ok := handler.Progress()
	if !ok || got != latest {
		t.Fatalf("progress = %+v (%v), want %+v", got, ok, latest)
	}
	if len(recorder.debugs) == 0 {
		t.Fatal("invalid progress frame not logged")
	}
	if recorder.payload.String() != protocol.EncodeProgress(protocol.Progress{Bytes: 99, Total: 99})+"\n" {
		t.Fatalf("unmarked look-alike not printed as payload: %q", recorder.payload.String())
	}
}

// TestMaprHandlerKeepsProgress checks that the MapReduce handler passes
// progress frames to the same handling.
func TestMaprHandlerKeepsProgress(t *testing.T) {
	want := protocol.Progress{Bytes: 5, Total: 5, Files: 1, FilesTotal: 1}
	handler := newProgressTestMaprHandler(t)
	if _, err := handler.Write(frame(marked(protocol.HiddenCapabilitiesPrefix),
		marked(protocol.EncodeProgress(want)))); err != nil {
		t.Fatal(err)
	}
	if got, ok := handler.Progress(); !ok || got != want {
		t.Fatalf("progress = %+v (%v), want %+v", got, ok, want)
	}
}

func newProgressTestMaprHandler(t *testing.T) *MaprHandler {
	t.Helper()
	query, err := mapr.NewQuery("select count(status) from stats group by status", logging.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	return NewMaprHandler("srv1", maprclient.NewSessionState(query, logging.NopLogger{}), &payloadRecorder{})
}
