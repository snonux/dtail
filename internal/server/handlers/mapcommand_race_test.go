package handlers

// TestHandleMapCommandShutdownRace is a negative test that exercises the race
// between handleMapCommand writing h.aggregate/h.turboAggregate and baseHandler
// Shutdown reading those same pointers concurrently. Running this test with
// -race detects the unsynchronized access before the atomic.Pointer fix is
// applied, and passes cleanly after.
//
// The test does not set up a real MapReduce query; instead it injects a stub
// aggregate via the atomic accessors so the test is self-contained and fast.

import (
	"sync"
	"testing"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/io/line"
	maprserver "github.com/mimecast/dtail/internal/mapr/server"
	userserver "github.com/mimecast/dtail/internal/user/server"
)

// TestAggregatePointerRaceWithShutdown concurrently writes h.aggregate and
// h.turboAggregate (as handleMapCommand does) while a goroutine reads them
// (as baseHandler.Shutdown and HasRegularAggregate/TurboAggregate do).
// With plain pointer fields this triggers the race detector; with
// atomic.Pointer the test passes cleanly.
func TestAggregatePointerRaceWithShutdown(t *testing.T) {
	resetServerLogger(t)

	const iterations = 200

	for i := 0; i < iterations; i++ {
		h := &baseHandler{
			done:           internal.NewDone(),
			lines:          make(chan *line.Line, 4),
			serverMessages: make(chan string, 8),
			maprMessages:   make(chan string, 4),
			user:           &userserver.User{Name: "race-test-user"},
		}

		// Build a real *server.Aggregate so the race involves actual heap
		// pointers rather than nil – a nil-only race is too easy to miss.
		agg, err := maprserver.NewAggregate("select count($0) from .", "")
		if err != nil {
			t.Skipf("could not create aggregate (query parse): %v", err)
		}

		var wg sync.WaitGroup

		// Writer goroutine – simulates handleMapCommand setting the aggregate.
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.setAggregate(agg)
		}()

		// Reader goroutine – simulates HasRegularAggregate / Shutdown reading.
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.getAggregate()
		}()

		wg.Wait()
		// Cleanup: shut down the aggregate so its internal goroutines exit.
		if got := h.getAggregate(); got != nil {
			got.Shutdown()
		}
	}
}

// TestTurboAggregatePointerRaceWithShutdown is the turbo-mode counterpart of
// TestAggregatePointerRaceWithShutdown.
func TestTurboAggregatePointerRaceWithShutdown(t *testing.T) {
	resetServerLogger(t)

	const iterations = 200

	for i := 0; i < iterations; i++ {
		h := &baseHandler{
			done:           internal.NewDone(),
			lines:          make(chan *line.Line, 4),
			serverMessages: make(chan string, 8),
			maprMessages:   make(chan string, 4),
			user:           &userserver.User{Name: "race-test-turbo-user"},
		}

		ta, err := maprserver.NewTurboAggregate("select count($0) from .", "")
		if err != nil {
			t.Skipf("could not create turbo aggregate: %v", err)
		}

		var wg sync.WaitGroup

		// Writer goroutine – simulates handleMapCommand setting the turbo aggregate.
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.setTurboAggregate(ta)
		}()

		// Reader goroutine – simulates TurboAggregate() / Shutdown reading.
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.getTurboAggregate()
		}()

		wg.Wait()
		// Cleanup: abort the turbo aggregate so its internal goroutines exit.
		if got := h.getTurboAggregate(); got != nil {
			got.Abort()
		}
	}
}
