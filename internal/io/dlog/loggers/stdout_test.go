package loggers

import (
	"testing"
	"time"
)

// Regression: during an interactive prompt, dlog.Common.Pause() unblocks when some
// goroutine hits stdout.log(); that goroutine must not hold the stdout mutex while
// waiting on resume, or dlog.Client.Info from the prompt callback deadlocks forever.
func TestStdoutSecondLogDuringPauseWaitDoesNotDeadlock(t *testing.T) {
	s := newStdout()

	go s.Pause()
	time.Sleep(50 * time.Millisecond)

	go func() {
		s.Log(time.Now(), "first log consumes pause and waits on resume")
	}()
	time.Sleep(50 * time.Millisecond)

	secondDone := make(chan struct{})
	go func() {
		s.Log(time.Now(), "second log must acquire mutex while first waits for Resume")
		close(secondDone)
	}()

	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: second Log blocked on mutex while first waits for Resume")
	}

	s.Resume()
	time.Sleep(50 * time.Millisecond)
}
