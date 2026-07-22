package handlers

import (
	"context"
	"encoding/base64"
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
	resetServerLogger(t)

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

	// Warm up so any lazily-started runtime goroutines are already up.
	_, cancel := h.newCommandContext(context.Background())
	cancel()
	time.Sleep(20 * time.Millisecond)

	baseline := runtime.NumGoroutine()

	const N = 100
	for i := 0; i < N; i++ {
		_, cancel := h.newCommandContext(context.Background())
		cancel()
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if delta := runtime.NumGoroutine() - baseline; delta <= 4 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watcher goroutines leaked: delta=%d (expected <= 4)", runtime.NumGoroutine()-baseline)
}

// TestNewCommandContextHandlerShutdownReleasesWatcher verifies the
// defensive safety net: if a caller forgets to cancel a per-command
// context, shutting down the handler still drains the watcher goroutine
// rather than leaving it blocked until process exit.
func TestNewCommandContextHandlerShutdownReleasesWatcher(t *testing.T) {
	h := &baseHandler{done: internal.NewDone()}

	// Warm up.
	_, cancel := h.newCommandContext(context.Background())
	cancel()
	time.Sleep(20 * time.Millisecond)

	baseline := runtime.NumGoroutine()

	const N = 50
	ctxs := make([]context.Context, 0, N)
	for i := 0; i < N; i++ {
		ctx, _ := h.newCommandContext(context.Background())
		ctxs = append(ctxs, ctx)
	}

	if delta := runtime.NumGoroutine() - baseline; delta < N/2 {
		t.Fatalf("expected goroutines to accumulate before shutdown, delta=%d", delta)
	}

	h.done.Shutdown()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if delta := runtime.NumGoroutine() - baseline; delta <= 4 {
			// All watcher goroutines should have observed the contexts
			// being cancelled via the defensive done.Done() branch.
			for _, ctx := range ctxs {
				select {
				case <-ctx.Done():
				default:
					t.Fatalf("context not cancelled by handler shutdown")
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watcher goroutines leaked past shutdown: delta=%d", runtime.NumGoroutine()-baseline)
}
