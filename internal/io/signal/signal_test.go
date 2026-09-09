package signal

import (
	"testing"
	"time"
)

func TestForceExitUsesFailureStatus(t *testing.T) {
	status := 0
	forceExit(func(code int) { status = code })
	if status != 1 {
		t.Fatalf("forced exit status = %d, want 1", status)
	}
}

func TestForceExitAfterUsesFailureStatusWithoutDelay(t *testing.T) {
	wait := make(chan time.Time)
	close(wait)
	status := 0
	forceExitAfter(wait, func(code int) { status = code })
	if status != 1 {
		t.Fatalf("delayed forced exit status = %d, want 1", status)
	}
}
