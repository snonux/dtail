package loggers

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestFileLoggerRotateDoesNotBlockWithoutWrites verifies that Rotate() does
// not deadlock when no log messages have been produced. Previously rotateCh
// was unbuffered and only drained opportunistically from write(), so a SIGHUP
// before any Log() call would block the caller forever.
func TestFileLoggerRotateDoesNotBlockWithoutWrites(t *testing.T) {
	f := newFile(Strategy{Rotation: SignalRotation, FileBase: "unit-test"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	f.Start(ctx, &wg)

	done := make(chan struct{})
	go func() {
		f.Rotate()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Rotate() blocked without any writes; expected prompt return")
	}

	cancel()
	wg.Wait()
}

// TestFileLoggerCancelBeforeFirstWriteDoesNotPanic verifies that cancelling
// the context before any write has happened does not panic. Previously the
// goroutine called f.fd.Close() unconditionally, but f.fd is only populated
// by the first getWriter() call, so a ctx cancel with no prior writes
// panicked on a nil pointer.
func TestFileLoggerCancelBeforeFirstWriteDoesNotPanic(t *testing.T) {
	f := newFile(Strategy{Rotation: SignalRotation, FileBase: "unit-test"})

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	f.Start(ctx, &wg)

	cancel()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(1 * time.Second):
		t.Fatal("file logger goroutine did not exit after ctx cancel")
	}
}
