package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/protocol"
)

// TestHandleCommandCancelsContextAfterCommandFinished verifies that
// baseHandler.handleCommand no longer discards the cancel func returned by
// newCommandContext. Pre-fix the cancel was dropped, so the per-command
// context (and the watcher goroutine spawned by newCommandContext) leaked
// for the lifetime of the SSH session. The cancel must fire when
// commandFinished is invoked.
func TestHandleCommandCancelsContextAfterCommandFinished(t *testing.T) {

	handler := newSessionTestHandler("handle-command-cancel-user")
	readServerMessage(t, handler.serverMessages)
	handler.handleCommandCb = handler.handleUserCommand

	type captured struct {
		ctx    context.Context
		finish func()
	}
	ch := make(chan captured, 1)
	handler.commands = map[string]commandHandler{
		// AUTHKEY is a side-effect command so commandFinished does not
		// trigger handler shutdown, keeping the test focused on the
		// per-command cancel contract.
		"AUTHKEY": func(ctx context.Context, _ lcontext.LContext, _ int, _ []string, commandFinished func()) {
			ch <- captured{ctx: ctx, finish: commandFinished}
		},
	}

	encoded := base64.StdEncoding.EncodeToString([]byte("AUTHKEY dummy"))
	handler.handleCommand("protocol " + protocol.ProtocolCompat + " base64 " + encoded)

	var got captured
	select {
	case got = <-ch:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("AUTHKEY command was not dispatched")
	}

	select {
	case <-got.ctx.Done():
		t.Fatal("per-command context cancelled before commandFinished ran")
	default:
	}

	got.finish()

	select {
	case <-got.ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("per-command context was not cancelled after commandFinished ran; cancel was discarded")
	}
}

// TestNewCommandContextReleasesWatcherGoroutine ensures the watcher
// goroutine spawned by newCommandContext exits promptly once either the
// per-command cancel fires or the handler is shut down. This is the
// defensive safety net that keeps a leak from accumulating even if a
// future caller forgets to invoke cancel.
func TestNewCommandContextReleasesWatcherGoroutine(t *testing.T) {
	h := &baseHandler{done: internal.NewDone()}
	t.Cleanup(h.done.Shutdown)

	baseline := runtime.NumGoroutine()

	const N = 100
	for i := 0; i < N; i++ {
		_, cancel := h.newCommandContext(context.Background())
		cancel()
	}

	waitForHandlerCondition(t, time.Second, "watcher goroutines did not exit after cancellation", func() bool {
		return runtime.NumGoroutine()-baseline <= 4
	}, func() string {
		return fmt.Sprintf("goroutine delta=%d", runtime.NumGoroutine()-baseline)
	})
}

// TestNewCommandContextHandlerShutdownReleasesWatcher verifies the
// defensive safety net: if a caller forgets to cancel a per-command
// context, shutting down the handler still drains the watcher goroutine
// rather than leaving it blocked until process exit.
func TestNewCommandContextHandlerShutdownReleasesWatcher(t *testing.T) {
	h := &baseHandler{done: internal.NewDone()}

	baseline := runtime.NumGoroutine()

	const N = 50
	ctxs := make([]context.Context, 0, N)
	for i := 0; i < N; i++ {
		ctx, _ := h.newCommandContext(context.Background())
		ctxs = append(ctxs, ctx)
	}

	waitForHandlerCondition(t, time.Second, "command watchers did not start", func() bool {
		return runtime.NumGoroutine()-baseline >= N/2
	}, func() string {
		return fmt.Sprintf("goroutine delta=%d, want at least %d", runtime.NumGoroutine()-baseline, N/2)
	})

	h.done.Shutdown()

	deadline := time.Now().Add(time.Second)
	for i, ctx := range ctxs {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("context %d not cancelled by handler shutdown", i)
		}
		select {
		case <-ctx.Done():
		case <-time.After(remaining):
			t.Fatalf("context %d not cancelled by handler shutdown", i)
		}
	}

	waitForHandlerCondition(t, time.Until(deadline), "watcher goroutines leaked past shutdown", func() bool {
		return runtime.NumGoroutine()-baseline <= 4
	}, func() string {
		return fmt.Sprintf("goroutine delta=%d", runtime.NumGoroutine()-baseline)
	})
}
