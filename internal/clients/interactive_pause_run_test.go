package clients

import (
	"bytes"
	"testing"
)

// TestHoldOutputKeepsPauses: holdOutput keeps the output paused during fn
// (the logger's pause depth and the gate as they were, whether the pause is
// explicit or for typing, and whatever else pauses the logger: the host key
// prompt or the interrupt statistics), but has the logger and the gate hold
// what is written; afterwards neither holds any more and the pause state is
// as before. Output that is not paused is left alone.
func TestHoldOutputKeepsPauses(t *testing.T) {
	for _, tt := range []struct {
		name   string
		typing bool
		extra  int
	}{
		{name: "explicit"},
		{name: "typing", typing: true},
		{name: "explicit with another pause", extra: 1},
		{name: "typing with two other pauses", typing: true, extra: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger := newPausingTestLogger()
			client := newPauseTestClient(logger)
			pause := client.newInteractivePause()
			for range tt.extra {
				logger.Pause()
			}
			if tt.typing {
				pause.pauseTyping()
			} else {
				pause.pause()
			}
			gate := client.runtime.outputGate
			var during int
			var writable, gateHeld bool
			pause.holdOutput(func() {
				during = logger.pauseDepth()
				writable = logger.writable()
				gateHeld = gate.admit(&bytes.Buffer{}, []byte("x"))
			})
			if during != tt.extra+1 || !writable || !gateHeld {
				t.Fatalf("during fn: depth %d logger writable %v gate held %v, want depth %d and both holding",
					during, writable, gateHeld, tt.extra+1)
			}
			if logger.pauseDepth() != tt.extra+1 || !pause.paused || pause.typing != tt.typing || !gate.paused ||
				logger.writable() || gate.hold != nil {
				t.Fatalf("after: depth %d paused %v typing %v gate %v logger writable %v gate hold %v",
					logger.pauseDepth(), pause.paused, pause.typing, gate.paused, logger.writable(), gate.hold)
			}
		})
	}

	logger := newPausingTestLogger()
	client := newPauseTestClient(logger)
	pause := client.newInteractivePause()
	ran := false
	pause.holdOutput(func() { ran = true })
	if !ran || pause.paused || logger.pauseDepth() != 0 || client.runtime.outputGate.paused {
		t.Fatalf("not paused: ran %v paused %v depth %d", ran, pause.paused, logger.pauseDepth())
	}
}
