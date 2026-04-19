package server

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/source"
)

func TestAggregateNextLineWaitsForLateNextChannel(t *testing.T) {
	t.Parallel()

	current := make(chan *line.Line)
	next := make(chan *line.Line)
	close(current)

	agg := &Aggregate{
		NextLinesCh: make(chan chan *line.Line, 1),
		linesCh:     current,
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		agg.NextLinesCh <- next
	}()

	got, ok, noMoreChannels := agg.nextLine()
	if got != nil {
		t.Fatalf("expected no line while switching channels, got %#v", got)
	}
	if ok {
		t.Fatal("expected ok=false while switching to the next channel")
	}
	if noMoreChannels {
		t.Fatal("expected late-arriving next channel to be picked up")
	}
	if agg.linesCh != next {
		t.Fatal("expected aggregate to switch to the late-arriving next channel")
	}
}

func TestAggregateNextLineDoesNotAbandonIdleCurrentChannel(t *testing.T) {
	t.Parallel()

	current := make(chan *line.Line)
	next := make(chan *line.Line, 1)
	next <- line.Null()

	agg := &Aggregate{
		NextLinesCh: make(chan chan *line.Line, 1),
		linesCh:     current,
	}
	agg.NextLinesCh <- next

	got, ok, noMoreChannels := agg.nextLine()
	if got != nil {
		t.Fatalf("expected no line from an idle current channel, got %#v", got)
	}
	if ok {
		t.Fatal("expected ok=false for an idle current channel")
	}
	if noMoreChannels {
		t.Fatal("expected aggregate to keep waiting on the current channel")
	}
	if agg.linesCh != current {
		t.Fatal("expected aggregate to stay on the current channel until it closes")
	}
}

// ensureAggregateTestConfig initialises the minimum globals required by
// aggregate tests. Safe to call from multiple tests; it is idempotent.
func ensureAggregateTestConfig(t *testing.T) {
	t.Helper()
	if config.Common == nil {
		config.Common = &config.CommonConfig{
			Logger:   "none",
			LogLevel: "error",
		}
	}
	if config.Server == nil {
		config.Server = &config.ServerConfig{
			MapreduceLogFormat: "default",
		}
	}
	if dlog.Server == nil {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		var wg sync.WaitGroup
		wg.Add(1)
		dlog.Start(ctx, &wg, source.Server)
	}
}

// TestAggregateStartFlushesOnCtxCancel is a negative test that verifies
// aggregated data is NOT dropped when the context is cancelled before the
// fieldsCh is closed. Prior to the fix, the ctx.Done() branch in
// aggregateAndSerialize returned without calling serialize(), silently losing
// any data accumulated in the current interval.
func TestAggregateStartFlushesOnCtxCancel(t *testing.T) {
	ensureAggregateTestConfig(t)

	// A simple count query whose log format matches the test lines below.
	queryStr := `from STATS select count($time) from - group by $time`
	agg, err := NewAggregate(queryStr, config.Server.MapreduceLogFormat)
	if err != nil {
		t.Fatalf("NewAggregate: %v", err)
	}

	// maprMessages must be buffered large enough so the best-effort final
	// serialize can complete without a receiver goroutine.
	maprMessages := make(chan string, 64)

	ctx, cancel := context.WithCancel(context.Background())

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		agg.Start(ctx, maprMessages)
	}()

	// Feed a line channel with real MAPREDUCE log lines so aggregateAndSerialize
	// accumulates at least one group entry before we cancel the context.
	lines := make(chan *line.Line, 8)
	agg.NextLinesCh <- lines

	// DTail MAPREDUCE format line — matches the "default" log format parser.
	const testLine = "INFO|1002-071143|1|stats.go:56|8|15|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1"
	lines <- line.New(bytes.NewBufferString(testLine), 1, 100, "test")

	// Allow the aggregate goroutine to process the line before cancelling.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait for Start to return.
	select {
	case <-startDone:
	case <-time.After(2 * time.Second):
		t.Fatal("aggregate.Start did not return after ctx cancel")
	}

	// Drain the messages channel (no more senders at this point).
	close(maprMessages)
	var results []string
	for msg := range maprMessages {
		results = append(results, msg)
	}

	// The fix must have flushed the accumulated group; we expect at least one
	// serialized message containing the count field.
	foundCount := false
	for _, r := range results {
		if strings.Contains(r, "count($time)") {
			foundCount = true
			break
		}
	}
	if !foundCount {
		t.Errorf("expected last-interval aggregate data to be flushed on ctx cancel, got %d messages: %v",
			len(results), results)
	}
}
