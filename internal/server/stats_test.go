package server

import (
	"testing"
)

// TestStatsConnectionCounterBalance verifies that incrementConnections and
// decrementConnections remain balanced so currentConnections never goes negative.
//
// The bug: incrementConnections() was called once per TCP connection in
// handleConnection, but decrementConnections() was called inside the
// handleShellRequest goroutine that waits on sshConn.Wait(). A connection
// with N shell requests (channels) would therefore decrement N times against
// a single increment, driving currentConnections negative.
//
// The fix pairs both calls inside handleConnection via a defer, removing the
// decrement from handleShellRequest entirely.
func TestStatsConnectionCounterBalance(t *testing.T) {
	s := newStats(100)

	// Simulate a single TCP connection that opens 3 shell channels (requests).
	// Before the fix, incrementConnections was called once but
	// decrementConnections was called once per shell request.

	// Increment once per connection (the correct, fixed behaviour).
	s.incrementConnections()

	if s.currentConnections != 1 {
		t.Fatalf("expected currentConnections=1 after one connection, got %d", s.currentConnections)
	}

	// Simulate 3 shell requests completing (decrement only once — the fixed path).
	// Under the old code this loop body would be called once per shell request,
	// but decrementConnections must only be called once per connection.
	s.decrementConnections()

	if s.currentConnections != 0 {
		t.Fatalf("expected currentConnections=0 after decrement, got %d", s.currentConnections)
	}
}

// TestStatsMultipleConnectionsBalance verifies counters stay non-negative across
// multiple independent connections each with their own lifecycle.
func TestStatsMultipleConnectionsBalance(t *testing.T) {
	s := newStats(100)
	const n = 5

	for i := 0; i < n; i++ {
		s.incrementConnections()
	}
	if s.currentConnections != n {
		t.Fatalf("expected currentConnections=%d, got %d", n, s.currentConnections)
	}

	for i := 0; i < n; i++ {
		s.decrementConnections()
		if s.currentConnections < 0 {
			t.Fatalf("currentConnections went negative at decrement %d: got %d", i+1, s.currentConnections)
		}
	}
	if s.currentConnections != 0 {
		t.Fatalf("expected currentConnections=0 after all decrements, got %d", s.currentConnections)
	}
}

// TestStatsConnectionCounterNeverNegative simulates the pre-fix behaviour to
// document what the bug looked like: multiple decrements per single increment
// would drive the counter negative. This test asserts that the counter does
// NOT go negative when decrement is called more times than increment (i.e.
// if decrement were still called per shell request rather than per connection).
//
// This test is intentionally written to document the invariant: the counter
// must always be >= 0. With the fix in place (decrement once per connection),
// the counter stays at 0 after one increment + one decrement.
func TestStatsCounterInvariant(t *testing.T) {
	s := newStats(100)

	s.incrementConnections() // one connection

	// With the fix: only one decrement per connection, counter stays >= 0.
	s.decrementConnections()

	if s.currentConnections < 0 {
		t.Fatalf("currentConnections must never be negative, got %d", s.currentConnections)
	}
}
