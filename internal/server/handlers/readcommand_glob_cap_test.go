package handlers

// Tests for the glob expansion cap (MaxGlobTargets).
//
// Security context: an authenticated user with a broad read permission (e.g.
// "readfiles:^/.*") can craft a glob that matches thousands of paths. Before
// this fix, filepath.Glob would return every match, pendingFiles would be
// incremented by the full count, and one goroutine would be spawned per path
// — all before any concurrency limiter was consulted. The cap enforced here
// truncates the expansion to MaxGlobTargets paths so that the number of
// goroutines and the memory used by a single read command are bounded.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/lcontext"
	maprserver "github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// globCapTestServer is a minimal readCommandServer implementation whose sole
// purpose is to count PrepareReadTarget invocations (i.e. dispatched files).
// All output/aggregate/lifecycle methods are stubs. The catLimiter is
// generously sized so it never blocks the test.
type globCapTestServer struct {
	catLimiter    chan struct{}
	tailLimiter   chan struct{}
	serverMessage chan string
	// preparedCount counts how many file paths were dispatched for reading.
	preparedCount int32
	// pendingFiles mirrors the lifecycle counter used by readFiles.
	pendingFiles int32
	// maxGlobTargets is the cap returned by MaxGlobTargets().
	maxGlobTargets int
}

func newGlobCapTestServer(maxGlobTargets int) *globCapTestServer {
	// Use a large limiter so reads are never blocked by concurrency limits
	// — we only want to test the glob cap, not the semaphore.
	limiter := make(chan struct{}, 10000)
	return &globCapTestServer{
		catLimiter:     limiter,
		tailLimiter:    limiter,
		serverMessage:  make(chan string, 128),
		maxGlobTargets: maxGlobTargets,
	}
}

// PrepareReadTarget records the path and returns a valid journal-free target.
// We use a plain ValidatedReadTarget with FileKind so no actual file I/O is
// attempted; the goroutine that calls read() will fail gracefully when the
// nonexistent catFile cannot be opened, but that is fine for this test.
func (s *globCapTestServer) PrepareReadTarget(path string) (fs.ValidatedReadTarget, bool) {
	atomic.AddInt32(&s.preparedCount, 1)
	// Return a valid file-kind target so readFileIfPermissions proceeds past
	// the permission check and reaches the actual read machinery.
	return fs.ValidatedReadTarget{Kind: fs.FileKind}, true
}

func (s *globCapTestServer) CatLimiter() chan struct{} { return s.catLimiter }
func (s *globCapTestServer) TailLimiter() chan struct{} { return s.tailLimiter }

func (s *globCapTestServer) LogContext() interface{}        { return "glob-cap-test" }
func (s *globCapTestServer) SendServerMessage(msg string)   { s.drainOrStore(msg) }
func (s *globCapTestServer) ServerMessagesChannel() chan string { return s.serverMessage }
func (s *globCapTestServer) Hostname() string                { return "testhost" }
func (s *globCapTestServer) PlainOutput() bool               { return false }
func (s *globCapTestServer) Serverless() bool                { return false }

func (s *globCapTestServer) drainOrStore(msg string) {
	select {
	case s.serverMessage <- msg:
	default:
	}
}

func (s *globCapTestServer) Aggregate() *maprserver.Aggregate { return nil }

// AddPendingFiles tracks the lifecycle counter that readFiles uses to
// signal EOF; it operates on a separate field from preparedCount.
func (s *globCapTestServer) AddPendingFiles(delta int32) int32 {
	return atomic.AddInt32(&s.pendingFiles, delta)
}

func (s *globCapTestServer) CompletePendingFile() (int32, int32) {
	remaining := atomic.AddInt32(&s.pendingFiles, -1)
	return remaining, 0
}

func (s *globCapTestServer) PendingAndActive() (int32, int32) {
	return atomic.LoadInt32(&s.pendingFiles), 0
}
func (s *globCapTestServer) ActiveSessionGeneration() uint64      { return 0 }
func (s *globCapTestServer) TriggerShutdown()                     {}

func (s *globCapTestServer) DirectOutputActive() bool                     { return false }
func (s *globCapTestServer) EnableDirectOutput() bool                 { return false }
func (s *globCapTestServer) HasOutputEOF() bool                     { return false }
func (s *globCapTestServer) FlushOutput()                       {}
func (s *globCapTestServer) OutputEpoch() uint64                    { return 0 }
func (s *globCapTestServer) SignalOutputEOF(epoch uint64)           {}
func (s *globCapTestServer) GetOutputChannel() chan []byte           { return nil }
func (s *globCapTestServer) OutputChannelLen() int                  { return 0 }
func (s *globCapTestServer) WaitForOutputEOFAck(time.Duration) bool { return true }

func (s *globCapTestServer) ReadGlobRetryInterval() time.Duration      { return time.Millisecond }
func (s *globCapTestServer) ReadRetryInterval() time.Duration          { return time.Millisecond }
func (s *globCapTestServer) MaxLineLength() int                        { return 1024 * 1024 }
func (s *globCapTestServer) OutputTransmissionDelay() time.Duration { return time.Millisecond }
func (s *globCapTestServer) OutputEOFWaitDuration(int) time.Duration    { return time.Millisecond }
func (s *globCapTestServer) ShutdownSerializeWait() time.Duration { return time.Millisecond }
func (s *globCapTestServer) ShutdownIdleRecheckWait() time.Duration    { return time.Millisecond }
func (s *globCapTestServer) OutputEOFAckTimeout() time.Duration         { return time.Millisecond }

// MaxGlobTargets returns the configurable cap for this test server.
func (s *globCapTestServer) MaxGlobTargets() int { return s.maxGlobTargets }

// verify the interface is satisfied at compile time
var _ readCommandServer = (*globCapTestServer)(nil)

// createTempFiles creates n empty files named file0000.log … in dir and
// returns the glob pattern that matches all of them.
func createTempFiles(t *testing.T, dir string, n int) string {
	t.Helper()
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, fmt.Sprintf("file%04d.log", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatalf("create temp file %s: %v", name, err)
		}
	}
	return filepath.Join(dir, "*.log")
}

// TestGlobCapTruncatesExcessPaths verifies that when a glob matches more files
// than MaxGlobTargets, only MaxGlobTargets paths are dispatched. The check is
// done by counting PrepareReadTarget invocations on the mock server.
func TestGlobCapTruncatesExcessPaths(t *testing.T) {
	resetServerLogger(t)

	const (
		totalFiles = 20  // files on disk — clearly above the cap
		cap        = 5   // deliberately low cap to prove truncation
	)

	dir := t.TempDir()
	glob := createTempFiles(t, dir, totalFiles)

	srv := newGlobCapTestServer(cap)
	cmd := newReadCommand(srv, omode.CatClient)

	// Run readGlob with retries=1 so it finds files immediately.
	cmd.readGlob(context.Background(), lcontext.LContext{}, glob, regex.NewNoop(), 1)

	got := int(atomic.LoadInt32(&srv.preparedCount))
	if got > cap {
		t.Fatalf("glob cap not enforced: dispatched %d paths, expected at most %d", got, cap)
	}
	if got == 0 {
		t.Fatal("no paths were dispatched; expected exactly cap paths to be served")
	}
}

// TestGlobCapUnderLimitPassesAll verifies that when the number of glob matches
// is at or below MaxGlobTargets, all paths are dispatched without truncation.
func TestGlobCapUnderLimitPassesAll(t *testing.T) {
	resetServerLogger(t)

	const (
		totalFiles = 5
		cap        = 10 // cap is above totalFiles — nothing should be dropped
	)

	dir := t.TempDir()
	glob := createTempFiles(t, dir, totalFiles)

	srv := newGlobCapTestServer(cap)
	cmd := newReadCommand(srv, omode.CatClient)

	cmd.readGlob(context.Background(), lcontext.LContext{}, glob, regex.NewNoop(), 1)

	got := int(atomic.LoadInt32(&srv.preparedCount))
	if got != totalFiles {
		t.Fatalf("expected all %d paths dispatched, got %d", totalFiles, got)
	}
}

// TestGlobCapExactlyAtLimit verifies that when the number of glob matches
// equals MaxGlobTargets exactly, all paths are dispatched (boundary condition).
func TestGlobCapExactlyAtLimit(t *testing.T) {
	resetServerLogger(t)

	const count = 8 // cap == totalFiles

	dir := t.TempDir()
	glob := createTempFiles(t, dir, count)

	srv := newGlobCapTestServer(count)
	cmd := newReadCommand(srv, omode.CatClient)

	cmd.readGlob(context.Background(), lcontext.LContext{}, glob, regex.NewNoop(), 1)

	got := int(atomic.LoadInt32(&srv.preparedCount))
	if got != count {
		t.Fatalf("expected exactly %d paths dispatched (no truncation at limit), got %d", count, got)
	}
}
