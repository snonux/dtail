package clients

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

func TestNextRetryDelay(t *testing.T) {
	tests := []struct {
		name    string
		current time.Duration
		want    time.Duration
	}{
		{name: "zero uses initial", current: 0, want: initialRetryDelay},
		{name: "doubles normally", current: 4 * time.Second, want: 8 * time.Second},
		{name: "caps at max", current: 40 * time.Second, want: maxRetryDelay},
		{name: "stays max at max", current: maxRetryDelay, want: maxRetryDelay},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextRetryDelay(tt.current); got != tt.want {
				t.Fatalf("nextRetryDelay(%v) = %v, want %v", tt.current, got, tt.want)
			}
		})
	}
}

func TestJitterRetryDelayWithinBounds(t *testing.T) {
	base := 10 * time.Second
	random := rand.New(rand.NewSource(1))

	min := 8 * time.Second
	max := 12 * time.Second

	for i := 0; i < 100; i++ {
		got := jitterRetryDelay(base, random)
		if got < min || got > max {
			t.Fatalf("jitterRetryDelay() = %v, expected between %v and %v", got, min, max)
		}
	}
}

func TestSleepWithContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if sleepWithContext(ctx, time.Second) {
		t.Fatalf("sleepWithContext should stop when context is canceled")
	}

	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("sleepWithContext took too long to exit on canceled context")
	}
}
