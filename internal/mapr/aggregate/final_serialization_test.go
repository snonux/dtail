package aggregate

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
)

// finalTestGroups is the number of groups of the final serialization tests.
// At finalTestConsumerPause per message, a slow consumer takes 30 s (of the
// synctest bubble's fake clock) for them: three times the fixed 10 s deadline
// the final serialization used to have, which then dropped two thirds of the
// groups while the session still closed normally.
const (
	finalTestGroups        = 300
	finalTestConsumerPause = 100 * time.Millisecond
)

// newFinalTestAggregate returns an aggregate holding groups groups, one
// sample each, ready for a final serialization.
func newFinalTestAggregate(t *testing.T, groups int) *Aggregate {
	t.Helper()
	a, err := newAggregateFromTextForTest(
		`from STATS select count($time),$time group by $time interval 3600`,
		logging.NopLogger{},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	countField := a.query.Select[0].FieldStorage
	timeField := a.query.Select[1].FieldStorage
	a.serializer.groupMu.Lock()
	defer a.serializer.groupMu.Unlock()
	for i := range groups {
		key := fmt.Sprintf("group-%d", i)
		a.serializer.groupSets[key] = &mapr.AggregateSet{
			Samples: 1,
			FValues: map[string]float64{countField: 1},
			SValues: map[string]string{timeField: key},
		}
	}
	return a
}

// slowConsumer receives up to limit messages, pausing after each one like a
// client whose connection drains slowly, and reports how many it received.
// It stops early once stop is closed and no message is ready, so that a
// serialization that gave up fails the test instead of deadlocking it.
func slowConsumer(messages <-chan string, limit int, stop <-chan struct{}) <-chan int {
	received := make(chan int, 1)
	go func() {
		n := 0
		defer func() { received <- n }()
		for n < limit {
			select {
			case <-messages:
				n++
			case <-stop:
				return
			}
			time.Sleep(finalTestConsumerPause)
		}
	}()
	return received
}

// TestAggregateFinalSerializationHasNoDeadline sends a final result to a
// consumer far slower than the former 10 s bound, through both ways a final
// serialization starts (an explicit Shutdown and Start reacting to
// FinishInput). Every group must arrive and the result must be complete.
func TestAggregateFinalSerializationHasNoDeadline(t *testing.T) {
	tests := []struct {
		name     string
		finalize func(a *Aggregate, messages chan string)
	}{
		{name: "Shutdown", finalize: func(a *Aggregate, messages chan string) {
			a.PrepareOutput(context.Background(), messages)
			a.Shutdown(context.Background())
		}},
		{name: "Start after FinishInput", finalize: func(a *Aggregate, messages chan string) {
			a.FinishInput()
			a.Start(context.Background(), messages)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a := newFinalTestAggregate(t, finalTestGroups)
				messages := make(chan string)
				finalized := make(chan struct{})
				received := slowConsumer(messages, finalTestGroups, finalized)
				start := time.Now()
				tt.finalize(a, messages)
				elapsed := time.Since(start)
				close(finalized)

				if got := <-received; got != finalTestGroups {
					t.Fatalf("consumer received %d groups, want %d", got, finalTestGroups)
				}
				if !a.FinalResultComplete() {
					t.Fatal("FinalResultComplete() = false after every group was sent")
				}
				if minimum := 10 * time.Second; elapsed <= minimum {
					t.Fatalf("final serialization took %v, want more than %v to cover the former deadline",
						elapsed, minimum)
				}
			})
		})
	}
}

// TestAggregateFinalSerializationCanceledIsIncomplete cancels the
// finalization context part way through the groups: the serialization
// stops, keeps the unsent groups and reports the result as incomplete, which
// the session handler then reports to the client as a failed command.
func TestAggregateFinalSerializationCanceledIsIncomplete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const sent = 50
		a := newFinalTestAggregate(t, finalTestGroups)
		messages := make(chan string)
		a.PrepareOutput(context.Background(), messages)
		ctx, cancel := context.WithCancel(context.Background())
		received := slowConsumer(messages, sent, nil)
		go func() {
			<-received
			cancel()
		}()

		a.Shutdown(ctx)

		if a.FinalResultComplete() {
			t.Fatal("FinalResultComplete() = true after a canceled final serialization")
		}
		if got, want := a.countGroups(), finalTestGroups-sent; got != want {
			t.Fatalf("unsent groups kept = %d, want %d", got, want)
		}
	})
}

// TestAggregateFinalResultIncompleteWithoutFinalization pins that an
// aggregate whose terminal state is an abort, or that never finalized, has
// no complete final result.
func TestAggregateFinalResultIncompleteWithoutFinalization(t *testing.T) {
	a := newFinalTestAggregate(t, 1)
	if a.FinalResultComplete() {
		t.Fatal("FinalResultComplete() = true before finalization")
	}
	a.Abort()
	a.PrepareOutput(context.Background(), make(chan string, 1))
	a.Shutdown(context.Background())
	if a.FinalResultComplete() {
		t.Fatal("FinalResultComplete() = true after Abort won the terminal state")
	}
}
