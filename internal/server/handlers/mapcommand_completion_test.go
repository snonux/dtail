package handlers

// Regression tests for the server-mode output dmap deadlock: with direct output
// enabled, Aggregate.Start used to block until session teardown, so the
// map command never returned, the handler's active-command count never hit
// zero, and the session never shut down — the client hung forever after
// receiving all results. The bug survived because integration-test run mode
// force-disables output (internal/config/initializer.go), so this path was
// never exercised by the integration suite. These tests drive a real
// ServerHandler (real command dispatch, file reads, output aggregate,
// shutdown handshake) exactly like the SSH layer does, just without SSH.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/session"
	sshserver "github.com/mimecast/dtail/internal/ssh/server"
	userserver "github.com/mimecast/dtail/internal/user/server"
)

const testStatsLine = "INFO|1002-071143|1|stats.go:56|8|15|7|0.21|471h0m21s|" +
	"MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1"

// resetCommonLogger installs a quiet common logger for the duration of the
// test. The mapr serialization path logs via dlog.Common, which is nil unless
// a real logger was started; a zero-value DLog silently discards everything.
func resetCommonLogger(t *testing.T) {
	t.Helper()

	originalLogger := dlog.Common
	dlog.Common = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Common = originalLogger
	})
}

// newMapTestHandler builds a fully wired ServerHandler with direct output
// enabled, exactly as the SSH server would (via NewServerHandler). The user
// is the continuous-query user, which bypasses per-path permission checks so
// the test can read files from t.TempDir().
func newMapTestHandler(t *testing.T) *ServerHandler {
	t.Helper()
	resetServerLogger(t)
	resetCommonLogger(t)

	user := &userserver.User{Name: config.ContinuousUser}
	serverCfg := &config.ServerConfig{
		MapreduceLogFormat: "default",
		AuthKeyEnabled:     true,
	}
	return NewServerHandler(user, make(chan struct{}, 4), make(chan struct{}, 4),
		serverCfg, sshserver.NewAuthKeyStore(time.Hour, 5))
}

// wrapHandlerCommandsForJoin wraps every registered command handler so the
// test can wait for all asynchronously dispatched command goroutines to run
// to full completion (including their completion callbacks, which log and may
// trigger the session shutdown). Without this join a late dlog call from a
// command goroutine would race with the test-logger restore in cleanup.
func wrapHandlerCommandsForJoin(handler *ServerHandler) *sync.WaitGroup {
	wg := &sync.WaitGroup{}
	for name, origHandler := range handler.commands {
		origHandler := origHandler
		handler.commands[name] = func(ctx context.Context, ltx lcontext.LContext,
			argc int, args []string, commandFinished func()) {

			wg.Add(1)
			finished := func() {
				defer wg.Done()
				commandFinished()
			}
			origHandler(ctx, ltx, argc, args, finished)
		}
	}
	return wg
}

// waitForCommandJoin waits until every dispatched command goroutine has fully
// finished; failing the test on timeout instead of leaking goroutines.
func waitForCommandJoin(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for command goroutines to finish")
	}
}

// encodeTestCommand wraps a command in the client wire framing
// (protocol version + base64 + ';' delimiter), mirroring the client-side
// SendMessage implementation.
func encodeTestCommand(command string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	return fmt.Sprintf("protocol %s base64 %s;", protocol.ProtocolCompat, encoded)
}

// testOutput collects everything the handler sends to the "client".
type testOutput struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (o *testOutput) append(p []byte) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf.Write(p)
	return o.buf.String()
}

func (o *testOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (o *testOutput) waitForContains(t *testing.T, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !strings.Contains(o.String(), substr) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for output to contain %q; got: %q", substr, o.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startTestReader drains handler.Read like the SSH session output
// goroutine (io.Copy) does, collecting all output. When the server initiates
// the close handshake it replies with the client's close acknowledgement, so
// the shutdown sequence completes without waiting for the 5s ack timeout.
//
// writeMu stands in for the single SSH input goroutine of a real session:
// baseHandler.Write is not safe for concurrent use, so the test serializes
// its own command writes and the reader's ack write through this mutex.
func startTestReader(handler *ServerHandler, output *testOutput,
	writeMu *sync.Mutex) <-chan struct{} {

	readerDone := make(chan struct{})
	var ackOnce sync.Once
	go func() {
		defer close(readerDone)
		p := make([]byte, 4096)
		for {
			n, err := handler.Read(p)
			if n > 0 {
				all := output.append(p[:n])
				if strings.Contains(all, ".syn close connection") {
					ackOnce.Do(func() {
						writeMu.Lock()
						defer writeMu.Unlock()
						_, _ = handler.Write([]byte(encodeTestCommand(".ack close connection")))
					})
				}
			}
			if err == io.EOF {
				return
			}
		}
	}()
	return readerDone
}

func writeTestStatsFile(t *testing.T, lines int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stats.log")
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		sb.WriteString(testStatsLine)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write stats file: %v", err)
	}
	return path
}

// TestServerModeMapCommandCompletesSession runs a non-interactive dmap
// workload (legacy "map" + "cat" command stream) against a output-enabled
// handler and asserts that the session terminates on its own and delivers
// the aggregated result. Before the fix the session hung forever: the map
// command stayed blocked in Aggregate.Start after all input had been
// read, keeping the active-command count nonzero.
func TestServerModeMapCommandCompletesSession(t *testing.T) {
	handler := newMapTestHandler(t)
	path := writeTestStatsFile(t, 25)

	spec := session.Spec{
		Mode:  omode.MapClient,
		Files: []string{path},
		Query: "from STATS select count($time),$time group by $time",
		Regex: ".",
	}
	commands, err := spec.Commands()
	if err != nil {
		t.Fatalf("build commands: %v", err)
	}

	commandWg := wrapHandlerCommandsForJoin(handler)
	output := &testOutput{}
	var writeMu sync.Mutex
	readerDone := startTestReader(handler, output, &writeMu)

	var frames strings.Builder
	for _, command := range commands {
		frames.WriteString(encodeTestCommand(command))
	}
	writeMu.Lock()
	_, writeErr := handler.Write([]byte(frames.String()))
	writeMu.Unlock()
	if writeErr != nil {
		t.Fatalf("write commands: %v", writeErr)
	}

	select {
	case <-handler.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("session did not shut down after one-shot output map input was exhausted " +
			"(server-mode output dmap deadlock)")
	}

	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not observe EOF after handler shutdown")
	}
	waitForCommandJoin(t, commandWg, 10*time.Second)

	if !strings.Contains(output.String(), "count($time)≔25") {
		t.Fatalf("expected aggregated result count($time)≔25 in output, got: %q", output.String())
	}
}

// TestServerModeMapFollowSessionKeepsStreaming is the negative case for
// over-eager finalization: a continuous map query over a TAILED log never
// reaches input-exhausted, so the output aggregate must keep emitting
// interval results and the session must stay alive. The workload runs via
// SESSION START (the interactive-query bootstrap), matching how continuous
// queries are driven in practice.
func TestServerModeMapFollowSessionKeepsStreaming(t *testing.T) {
	handler := newMapTestHandler(t)
	path := writeTestStatsFile(t, 5)

	spec := session.Spec{
		Mode:  omode.TailClient,
		Files: []string{path},
		Query: "from STATS select count($time),$time group by $time interval 1",
		Regex: ".",
	}
	startCommand, err := spec.StartCommand()
	if err != nil {
		t.Fatalf("build session start command: %v", err)
	}

	commandWg := wrapHandlerCommandsForJoin(handler)
	output := &testOutput{}
	var writeMu sync.Mutex
	readerDone := startTestReader(handler, output, &writeMu)

	writeMu.Lock()
	_, writeErr := handler.Write([]byte(encodeTestCommand(startCommand)))
	writeMu.Unlock()
	if writeErr != nil {
		t.Fatalf("write session start: %v", writeErr)
	}
	output.waitForContains(t, sessionAckStartOKPrefix, 5*time.Second)

	// Keep appending lines like a live log file; the tailed input must keep
	// feeding the aggregate across serialization intervals.
	feederStop := make(chan struct{})
	feederDone := make(chan struct{})
	go func() {
		defer close(feederDone)
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer func() { _ = file.Close() }()
		for {
			select {
			case <-feederStop:
				return
			case <-time.After(50 * time.Millisecond):
				if _, err := file.WriteString(testStatsLine + "\n"); err != nil {
					return
				}
			}
		}
	}()

	// An interval-serialized interim aggregate result must arrive while the
	// stream is live (the fix must not finish a follow-mode aggregate).
	output.waitForContains(t, "count($time)≔", 15*time.Second)

	// The session must still be running: tailed input never exhausts.
	select {
	case <-handler.Done():
		t.Fatal("follow-mode output map session shut down prematurely (over-eager finalization)")
	default:
	}

	close(feederStop)
	<-feederDone

	// Tear down like a disconnecting client and join all command goroutines
	// so nothing outlives the test (the session keeps commands alive until
	// their contexts are cancelled by the handler shutdown).
	handler.done.Shutdown()
	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not observe EOF after handler shutdown")
	}
	waitForCommandJoin(t, commandWg, 10*time.Second)

	if pending, active := handler.PendingAndActive(); pending != 0 || active != 0 {
		t.Fatalf("handler did not quiesce: pending=%d active=%d", pending, active)
	}
}
