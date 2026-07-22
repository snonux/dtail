package handlers

// TestAggregatePointerRaceWithShutdown is a negative test that exercises
// the race between handleMapCommand writing h.aggregate and baseHandler
// Shutdown reading that same pointer concurrently. Running this test with -race
// detects unsynchronized access before the atomic.Pointer fix is applied, and
// passes cleanly after. The regular-aggregate counterpart was removed with the
// regular server.Aggregate itself (task hv0); output is now the only aggregate.
//
// The test does not set up a real MapReduce query; instead it injects a real
// output aggregate via the atomic accessors so the test is self-contained.

import (
	"sync"
	"testing"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/io/line"
	maprserver "github.com/mimecast/dtail/internal/mapr/server"
	userserver "github.com/mimecast/dtail/internal/user/server"
)

func TestAggregatePointerRaceWithShutdown(t *testing.T) {
	resetServerLogger(t)

	const iterations = 200

	for i := 0; i < iterations; i++ {
		h := &baseHandler{
			done:           internal.NewDone(),
			lines:          make(chan *line.Line, 4),
			serverMessages: make(chan string, 8),
			maprMessages:   make(chan string, 4),
			user:           &userserver.User{Name: "race-test-output-user"},
		}

		ta, err := maprserver.NewAggregate("select count($0) from .", "")
		if err != nil {
			t.Skipf("could not create output aggregate: %v", err)
		}

		var wg sync.WaitGroup

		// Writer goroutine – simulates handleMapCommand setting the output aggregate.
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.setAggregate(ta)
		}()

		// Reader goroutine – simulates Aggregate() / Shutdown reading.
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.getAggregate()
		}()

		wg.Wait()
		// Cleanup: abort the output aggregate so its internal goroutines exit.
		if got := h.getAggregate(); got != nil {
			got.Abort()
		}
	}
}
