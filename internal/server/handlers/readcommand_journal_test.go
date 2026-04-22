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

var _ readCommandServer = (*journalReadTestServer)(nil)

func TestReadCommandDispatchesJournalSpecWithoutGlob(t *testing.T) {
	resetServerLogger(t)

	argsFile := filepath.Join(t.TempDir(), "args")
	installHandlerFakeJournalctl(t, argsFile, "alpha\n")

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
	if got := strings.TrimSpace(readHandlerTestFile(t, argsFile)); got != "-u ssh.service" {
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

func emptyLContext() lcontext.LContext {
	return lcontext.LContext{}
}

func installHandlerFakeJournalctl(t *testing.T, argsFile, output string) {
	t.Helper()

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "journalctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$FAKE_JOURNAL_ARGS\"\nprintf '%s' \"$FAKE_JOURNAL_OUTPUT\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake journalctl: %v", err)
	}

	t.Setenv("FAKE_JOURNAL_ARGS", argsFile)
	t.Setenv("FAKE_JOURNAL_OUTPUT", output)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readHandlerTestFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
