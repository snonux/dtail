//go:build linux

package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/fs"
	journaltest "github.com/mimecast/dtail/internal/io/journal/testhelper"
	"github.com/mimecast/dtail/internal/lcontext"
	maprserver "github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/omode"
)

type journalReadTestServer struct {
	catLimiter    chan struct{}
	tailLimiter   chan struct{}
	outputLines    chan []byte
	serverMessage chan string
	prepared      []string
	pending       int32
	shutdowns     int32
}

func newJournalReadTestServer() *journalReadTestServer {
	return &journalReadTestServer{
		catLimiter:    make(chan struct{}, 1),
		tailLimiter:   make(chan struct{}, 1),
		outputLines:    make(chan []byte, 16),
		serverMessage: make(chan string, 16),
	}
}

func (s *journalReadTestServer) LogContext() interface{} {
	return "journal-read-test"
}

func (s *journalReadTestServer) PrepareReadTarget(path string) (fs.ValidatedReadTarget, bool) {
	s.prepared = append(s.prepared, path)
	target, err := fs.NewValidatedJournalTarget(path)
	return target, err == nil
}

func (s *journalReadTestServer) CatLimiter() chan struct{} {
	return s.catLimiter
}

func (s *journalReadTestServer) TailLimiter() chan struct{} {
	return s.tailLimiter
}

func (s *journalReadTestServer) SendServerMessage(message string) {
	s.serverMessage <- message
}

func (s *journalReadTestServer) ServerMessagesChannel() chan string {
	return s.serverMessage
}

func (s *journalReadTestServer) Hostname() string {
	return "testhost"
}

func (s *journalReadTestServer) PlainOutput() bool {
	return false
}

func (s *journalReadTestServer) Serverless() bool {
	return false
}

func (s *journalReadTestServer) Aggregate() *maprserver.Aggregate {
	return nil
}

func (s *journalReadTestServer) AddPendingFiles(delta int32) int32 {
	return atomic.AddInt32(&s.pending, delta)
}

func (s *journalReadTestServer) CompletePendingFile() (int32, int32) {
	return atomic.AddInt32(&s.pending, -1), 0
}

func (s *journalReadTestServer) PendingAndActive() (int32, int32) {
	return atomic.LoadInt32(&s.pending), 0
}

func (s *journalReadTestServer) ActiveSessionGeneration() uint64 {
	return 0
}

func (s *journalReadTestServer) TriggerShutdown() {
	atomic.AddInt32(&s.shutdowns, 1)
}

func (s *journalReadTestServer) DirectOutputActive() bool {
	return false
}

func (s *journalReadTestServer) EnableDirectOutput() bool { return false }

func (s *journalReadTestServer) HasOutputEOF() bool {
	return false
}

func (s *journalReadTestServer) FlushOutput() {}

func (s *journalReadTestServer) OutputEpoch() uint64 { return 0 }

func (s *journalReadTestServer) SignalOutputEOF(epoch uint64) {}

func (s *journalReadTestServer) GetOutputChannel() chan []byte {
	return s.outputLines
}

func (s *journalReadTestServer) OutputChannelLen() int {
	return 0
}

func (s *journalReadTestServer) WaitForOutputEOFAck(time.Duration) bool {
	return true
}

func (s *journalReadTestServer) ReadGlobRetryInterval() time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) ReadRetryInterval() time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) MaxLineLength() int {
	return 1024 * 1024
}

func (s *journalReadTestServer) OutputTransmissionDelay() time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) OutputEOFWaitDuration(int) time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) ShutdownSerializeWait() time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) ShutdownIdleRecheckWait() time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) OutputEOFAckTimeout() time.Duration {
	return time.Millisecond
}

// MaxGlobTargets returns a permissive cap suitable for journal test scenarios.
func (s *journalReadTestServer) MaxGlobTargets() int {
	return 1000
}

var _ readCommandServer = (*journalReadTestServer)(nil)

func TestReadCommandDispatchesJournalSpecWithoutGlob(t *testing.T) {
	resetServerLogger(t)

	mock := journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines: []string{"alpha"},
		},
	})

	server := newJournalReadTestServer()
	command := newReadCommand(server, omode.CatClient)
	command.Start(
		context.Background(),
		emptyLContext(),
		3,
		[]string{"cat", "journal:ssh.service", ""},
		1,
	)

	if got, want := server.prepared, []string{"journal:ssh.service"}; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("prepared targets = %q, want %q", got, want)
	}
	if got := strings.TrimSpace(mock.Args(t)); got != "-u ssh.service" {
		t.Fatalf("journalctl args = %q, want %q", got, "-u ssh.service")
	}

	// Output is now the one and only read path, so journal output is delivered as
	// protocol-formatted bytes on the output channel rather than as *line.Line on
	// the shared lines channel. The payload still carries the journal sourceID
	// and the line content, which is the real regression coverage here.
	got := waitForOutputLine(t, server.outputLines)
	if !strings.Contains(got, "journal:ssh.service") {
		t.Fatalf("output output missing journal sourceID; got %q", got)
	}
	if !strings.Contains(got, "alpha") {
		t.Fatalf("output output missing line content %q; got %q", "alpha", got)
	}
}

func TestReadCommandPassesJournalUnitAsSingleArgWithoutShell(t *testing.T) {
	resetServerLogger(t)

	marker := filepath.Join(t.TempDir(), "journal-injection-marker")
	unit := "ssh.service;touch${IFS}" + marker
	spec := fs.JournalSpecPrefix + unit

	mock := journaltest.InstallMock(t, journaltest.Scenario{
		Units: map[string]journaltest.Invocation{
			unit: {
				Lines: []string{"safe"},
			},
		},
	})

	server := newJournalReadTestServer()
	command := newReadCommand(server, omode.CatClient)
	command.Start(
		context.Background(),
		emptyLContext(),
		3,
		[]string{"cat", spec, ""},
		1,
	)

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("journal unit was interpreted as shell; marker stat error = %v", err)
	}

	unitFile, err := os.ReadFile(mock.UnitFile)
	if err != nil {
		t.Fatalf("read mock unit file: %v", err)
	}
	if got := strings.TrimSpace(string(unitFile)); got != unit {
		t.Fatalf("journalctl unit arg = %q, want %q", got, unit)
	}

	// See the note in TestReadCommandDispatchesJournalSpecWithoutGlob: output is
	// the only read path now, so the line content arrives as protocol-formatted
	// bytes on the output channel.
	got := waitForOutputLine(t, server.outputLines)
	if !strings.Contains(got, "safe") {
		t.Fatalf("output output missing line content %q; got %q", "safe", got)
	}
}

func emptyLContext() lcontext.LContext {
	return lcontext.LContext{}
}

// waitForOutputLine drains one payload from the output channel used by the
// single (former "output") read path. Session generation is 0 in these tests,
// so encodeGeneratedBytes leaves the payload unmodified and it can be compared
// as-is.
func waitForOutputLine(t *testing.T, outputLines <-chan []byte) string {
	t.Helper()
	select {
	case payload := <-outputLines:
		return string(payload)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for journal output")
		return ""
	}
}
