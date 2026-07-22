package cli

import (
	"runtime"
	"testing"
)

// TestEnableProfilingRatesSetsMutexFraction verifies that EnableProfilingRates
// actually turns on mutex profiling. Without this the /debug/pprof/mutex
// endpoint reports an empty profile. runtime.SetMutexProfileFraction(-1) reads
// the current fraction without changing it, so it lets us assert the state.
func TestEnableProfilingRatesSetsMutexFraction(t *testing.T) {
	// Save and restore global runtime state so this test does not leak into
	// other tests in the package. The block profile rate has no getter, so we
	// simply disable it again on cleanup.
	prevMutex := runtime.SetMutexProfileFraction(-1)
	t.Cleanup(func() {
		runtime.SetMutexProfileFraction(prevMutex)
		runtime.SetBlockProfileRate(0)
	})

	// Start from a known-disabled state.
	runtime.SetMutexProfileFraction(0)

	EnableProfilingRates()

	if got := runtime.SetMutexProfileFraction(-1); got != mutexProfileFraction {
		t.Errorf("mutex profile fraction = %d, want %d", got, mutexProfileFraction)
	}
}

// TestProfilingRateConstants pins the ss0-validated values so a change is a
// conscious decision rather than an accident.
func TestProfilingRateConstants(t *testing.T) {
	if mutexProfileFraction != 5 {
		t.Errorf("mutexProfileFraction = %d, want 5", mutexProfileFraction)
	}
	if blockProfileRateNanos != 100000 {
		t.Errorf("blockProfileRateNanos = %d, want 100000", blockProfileRateNanos)
	}
}
