package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
)

func TestSameCalendarDay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    time.Time
		b    time.Time
		want bool
	}{
		{
			name: "same day",
			a:    time.Date(2026, time.January, 15, 10, 0, 0, 0, time.UTC),
			b:    time.Date(2026, time.January, 15, 23, 59, 59, 0, time.UTC),
			want: true,
		},
		{
			name: "same day-of-month in different months",
			a:    time.Date(2026, time.January, 15, 10, 0, 0, 0, time.UTC),
			b:    time.Date(2026, time.February, 15, 10, 0, 0, 0, time.UTC),
			want: false,
		},
		{
			name: "same day-of-month across years",
			a:    time.Date(2025, time.December, 31, 10, 0, 0, 0, time.UTC),
			b:    time.Date(2026, time.January, 31, 10, 0, 0, 0, time.UTC),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := sameCalendarDay(tt.a, tt.b); got != tt.want {
				t.Fatalf("sameCalendarDay(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestContinuousRunJobsReleasesDayChangeWatcherAcrossRetries(t *testing.T) {
	dlog.Server = &dlog.DLog{}

	c := newContinuous(config.RuntimeConfig{
		Server: &config.ServerConfig{
			SSHBindAddress: "127.0.0.1",
		},
	})
	c.retryInterval = 25 * time.Millisecond

	var watcherStarts int32
	var watcherExits int32
	started := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	c.newMaprClient = func(args config.Args, mode clients.MaprClientMode) (continuousClient, error) {
		return blockingContinuousClient{
			started: started,
			release: release,
		}, nil
	}
	c.dayChangeWatcher = func(ctx context.Context) bool {
		atomic.AddInt32(&watcherStarts, 1)
		defer atomic.AddInt32(&watcherExits, 1)
		return c.waitForDayChange(ctx)
	}

	job := config.Continuous{}
	job.Enable = true
	job.RestartOnDayChange = true
	c.cfg.Server.Continuous = []config.Continuous{job}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runJobs(ctx)
		close(done)
	}()

	for i := int32(1); i <= 5; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for retry %d to start", i)
		}

		waitForCounterAtLeast(t, func() int32 {
			return atomic.LoadInt32(&watcherStarts)
		}, i)

		release <- struct{}{}

		waitForCounterAtLeast(t, func() int32 {
			return atomic.LoadInt32(&watcherExits)
		}, i)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("continuous job runner did not stop after cancellation")
	}
}

type blockingContinuousClient struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (f blockingContinuousClient) Start(context.Context, <-chan string) int {
	f.started <- struct{}{}
	<-f.release
	return 0
}

func waitForCounterAtLeast(t *testing.T, current func() int32, min int32) {
	t.Helper()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if current() >= min {
			return
		}

		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for counter to reach %d, got %d", min, current())
		case <-ticker.C:
		}
	}
}
