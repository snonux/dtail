package handlers

// Test pinning the load-bearing call ordering in readFiles' output EOF
// epilogue: the handshake epoch (OutputEpoch) MUST be captured before the
// pending-work check (PendingAndActive). Joiners increment the pending count
// before enabling output mode, so capturing the epoch first guarantees that a
// joiner invisible to the pending==0 check bumps the epoch after the capture,
// turning the stale SignalOutputEOF into a no-op. Reordering the two calls
// would silently reopen the mid-batch EOF window; this test fails if someone
// does that.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// epochOrderTestServer wraps globCapTestServer and records the order of the
// output-handshake-relevant calls made by readFiles. It reports an enabled
// output session (DirectOutputActive true, HasOutputEOF
// true) so readFiles runs the full EOF epilogue.
type epochOrderTestServer struct {
	*globCapTestServer

	mu             sync.Mutex
	calls          []string
	signaledEpochs []uint64
	outputLines     chan []byte
}

func newEpochOrderTestServer() *epochOrderTestServer {
	return &epochOrderTestServer{
		globCapTestServer: newGlobCapTestServer(100),
		outputLines:        make(chan []byte, 100),
	}
}

func (s *epochOrderTestServer) record(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
}

func (s *epochOrderTestServer) recordedCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *epochOrderTestServer) DirectOutputActive() bool        { return true }
func (s *epochOrderTestServer) EnableDirectOutput() bool    { return false }
func (s *epochOrderTestServer) HasOutputEOF() bool        { return true }

func (s *epochOrderTestServer) GetOutputChannel() chan []byte { return s.outputLines }

// OutputEpoch returns a sentinel value so the test can also verify that the
// captured epoch is the one passed through to SignalOutputEOF.
func (s *epochOrderTestServer) OutputEpoch() uint64 {
	s.record("OutputEpoch")
	return 42
}

func (s *epochOrderTestServer) PendingAndActive() (int32, int32) {
	s.record("PendingAndActive")
	return s.globCapTestServer.PendingAndActive()
}

func (s *epochOrderTestServer) FlushOutput() {
	s.record("FlushOutput")
}

func (s *epochOrderTestServer) SignalOutputEOF(epoch uint64) {
	s.mu.Lock()
	s.signaledEpochs = append(s.signaledEpochs, epoch)
	s.mu.Unlock()
	s.record("SignalOutputEOF")
}

var _ readCommandServer = (*epochOrderTestServer)(nil)

// TestReadFilesCapturesEpochBeforePendingCheck drives readFiles over a real
// (empty) file with an enabled output session and asserts that the EOF
// epilogue runs exactly OutputEpoch -> PendingAndActive -> FlushOutput ->
// SignalOutputEOF, i.e. the epoch is captured before the pending check.
// Earlier PendingAndActive calls from the per-file phase are recorded too,
// which is why the assertion checks the trailing four calls.
func TestReadFilesCapturesEpochBeforePendingCheck(t *testing.T) {
	resetServerLogger(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create temp file: %v", err)
	}

	srv := newEpochOrderTestServer()
	cmd := newReadCommand(srv, omode.CatClient)
	cmd.readFiles(context.Background(), lcontext.LContext{}, []string{path}, path,
		regex.NewNoop(), time.Millisecond)

	calls := srv.recordedCalls()
	wantTail := []string{"OutputEpoch", "PendingAndActive", "FlushOutput", "SignalOutputEOF"}
	if len(calls) < len(wantTail) {
		t.Fatalf("EOF epilogue did not run, recorded calls: %v", calls)
	}
	tail := calls[len(calls)-len(wantTail):]
	for i, want := range wantTail {
		if tail[i] != want {
			t.Fatalf("EOF epilogue call order = %v, want trailing %v (epoch must be captured BEFORE the pending check)",
				tail, wantTail)
		}
	}

	srv.mu.Lock()
	signaled := append([]uint64(nil), srv.signaledEpochs...)
	srv.mu.Unlock()
	if len(signaled) != 1 || signaled[0] != 42 {
		t.Fatalf("SignalOutputEOF epochs = %v, want exactly [42] (the captured OutputEpoch value)", signaled)
	}
}
