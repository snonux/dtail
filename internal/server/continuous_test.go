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

func TestContinuousRunJobReleasesDayChangeWatcherAfterEachRun(t *testing.T) {
	dlog.Server = &dlog.DLog{}

	c := newContinuous(config.RuntimeConfig{
		Server: &config.ServerConfig{
			SSHBindAddress: "127.0.0.1",
		},
	})

	var started int32
	var active int32
	c.newMaprClient = func(args config.Args, mode clients.MaprClientMode) (continuousClient, error) {
		return fakeContinuousClient{}, nil
	}
	c.dayChangeWatcher = func(ctx context.Context) bool {
		atomic.AddInt32(&started, 1)
		atomic.AddInt32(&active, 1)
		defer atomic.AddInt32(&active, -1)

		<-ctx.Done()
		return true
	}

	job := &config.Continuous{}
	job.Enable = true
	job.RestartOnDayChange = true

	for i := 0; i < 10; i++ {
		c.runJob(context.Background(), job)
	}

	waitForCounter(t, func() int32 {
		return atomic.LoadInt32(&started)
	}, 10)
	waitForCounter(t, func() int32 {
		return atomic.LoadInt32(&active)
	}, 0)
}

type fakeContinuousClient struct{}

func (fakeContinuousClient) Start(context.Context, <-chan string) int {
	time.Sleep(5 * time.Millisecond)
	return 0
}

func waitForCounter(t *testing.T, current func() int32, want int32) {
	t.Helper()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if current() == want {
			return
		}

		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for counter to reach %d, got %d", want, current())
		case <-ticker.C:
		}
	}
}
