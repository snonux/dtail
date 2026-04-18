package server

import (
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/line"
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
