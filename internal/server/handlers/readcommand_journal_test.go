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
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	maprserver "github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/omode"
)

type journalReadTestServer struct {
	catLimiter    chan struct{}
	tailLimiter   chan struct{}
	lines         chan *line.Line
	serverMessage chan string
	prepared      []string
	pending       int32
	shutdowns     int32
}

func newJournalReadTestServer() *journalReadTestServer {
	return &journalReadTestServer{
		catLimiter:    make(chan struct{}, 1),
		tailLimiter:   make(chan struct{}, 1),
		lines:         make(chan *line.Line, 4),
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

func (s *journalReadTestServer) RegisterAggregateLines(chan *line.Line) {}

func (s *journalReadTestServer) SharedLinesChannel() chan *line.Line {
	return s.lines
}

func (s *journalReadTestServer) HasRegularAggregate() bool {
	return false
}

func (s *journalReadTestServer) TurboAggregate() *maprserver.TurboAggregate {
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

func (s *journalReadTestServer) TurboBoostDisabled() bool {
	return true
}

func (s *journalReadTestServer) IsTurboMode() bool {
	return false
}

func (s *journalReadTestServer) EnableTurboMode() {}

func (s *journalReadTestServer) HasTurboEOF() bool {
	return false
}

func (s *journalReadTestServer) FlushTurboData() {}

func (s *journalReadTestServer) SignalTurboEOF() {}

func (s *journalReadTestServer) GetTurboChannel() chan []byte {
	return nil
}

func (s *journalReadTestServer) TurboChannelLen() int {
	return 0
}

func (s *journalReadTestServer) WaitForTurboEOFAck(time.Duration) bool {
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

func (s *journalReadTestServer) AggregateLinesChannelBufferSize() int {
	return 4
}

func (s *journalReadTestServer) TurboDataTransmissionDelay() time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) TurboEOFWaitDuration(int) time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) ShutdownTurboSerializeWait() time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) ShutdownIdleRecheckWait() time.Duration {
	return time.Millisecond
}

func (s *journalReadTestServer) TurboEOFAckTimeout() time.Duration {
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

	select {
	case got := <-server.lines:
		defer got.Recycle()
		if got.SourceID != "journal:ssh.service" {
			t.Fatalf("line SourceID = %q, want %q", got.SourceID, "journal:ssh.service")
		}
		if got.Content.String() != "alpha\n" {
			t.Fatalf("line content = %q, want %q", got.Content.String(), "alpha\n")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for journal output")
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

	select {
	case got := <-server.lines:
		defer got.Recycle()
		if got.Content.String() != "safe\n" {
			t.Fatalf("line content = %q, want %q", got.Content.String(), "safe\n")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for journal output")
	}
}

func emptyLContext() lcontext.LContext {
	return lcontext.LContext{}
}
