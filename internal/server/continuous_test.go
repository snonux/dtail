package server

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
)

func TestContinuousRunJobsReleasesDayChangeWatcherAcrossRetries(t *testing.T) {
	dlog.Server = &dlog.DLog{}

	c := newContinuous(config.RuntimeConfig{
		Server: &config.ServerConfig{
			SSHBindAddress: "127.0.0.1",
		},
	})
	c.retryInterval = 10 * time.Millisecond

	var started int32
	c.newMaprClient = func(args config.Args, mode clients.MaprClientMode) (continuousClient, error) {
		return fakeContinuousClient{started: &started}, nil
	}

	job := config.Continuous{}
	job.Enable = true
	job.RestartOnDayChange = true
	c.cfg.Server.Continuous = []config.Continuous{job}

	time.Sleep(20 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runJobs(ctx)
		close(done)
	}()

	waitForCounterAtLeast(t, func() int32 {
		return atomic.LoadInt32(&started)
	}, 5)

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("continuous job runner did not stop after cancellation")
	}

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if delta := runtime.NumGoroutine() - baseline; delta <= 4 {
			return
		}

		select {
		case <-deadline.C:
			t.Fatalf("watcher goroutines leaked: delta=%d", runtime.NumGoroutine()-baseline)
		case <-ticker.C:
		}
	}
}

type fakeContinuousClient struct {
	started *int32
}

func (f fakeContinuousClient) Start(context.Context, <-chan string) int {
	atomic.AddInt32(f.started, 1)
	time.Sleep(5 * time.Millisecond)
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
