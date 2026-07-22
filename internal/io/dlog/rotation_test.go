package dlog

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestRotateLoopHandlesMultipleSignals is a regression test for a bug where
// rotateLoop returned after the first signal, so subsequent SIGHUPs were
// silently dropped. Sending two signals on rotateCh must result in two
// rotate() invocations before ctx is cancelled.
func TestRotateLoopHandlesMultipleSignals(t *testing.T) {
	prev := Common
	Common = &DLog{maxLevel: None}
	t.Cleanup(func() { Common = prev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rotateCh := make(chan os.Signal, 2)
	var count int32
	rotated := make(chan struct{}, 2)
	rotate := func() {
		atomic.AddInt32(&count, 1)
		rotated <- struct{}{}
	}

	done := make(chan struct{})
	go func() {
		rotateLoop(ctx, rotateCh, rotate)
		close(done)
	}()

	rotateCh <- os.Interrupt
	waitForRotate(t, rotated, "first")
	rotateCh <- os.Interrupt
	waitForRotate(t, rotated, "second")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("rotateLoop did not return after ctx cancel")
	}

	if got := atomic.LoadInt32(&count); got != 2 {
		t.Fatalf("rotate() called %d times, want 2", got)
	}
}

func waitForRotate(t *testing.T, rotated <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-rotated:
	case <-time.After(2 * time.Second):
		t.Fatalf("rotate() was not invoked for %s signal", label)
	}
}
