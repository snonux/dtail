package cli

import (
	"context"
	"net/http"
	_ "net/http/pprof" // Register pprof handlers when runtime pprof endpoint is enabled.
	"sync"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/profiling"
	"github.com/mimecast/dtail/internal/source"
)

// ClientRuntime owns common client command runtime components.
type ClientRuntime struct {
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	profiler       *profiling.Profiler
	profileEnabled bool
}

// NewClientRuntime starts logging and profiling for a client command.
func NewClientRuntime(parent context.Context, profileFlags profiling.Flags, profileName string) *ClientRuntime {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &ClientRuntime{
		ctx:            ctx,
		cancel:         cancel,
		profiler:       profiling.NewProfiler(profileFlags.ToConfig(profileName)),
		profileEnabled: profileFlags.Enabled(),
	}

	runtime.wg.Add(1)
	dlog.Start(ctx, &runtime.wg, source.Client)
	return runtime
}

// Context returns the runtime context.
func (r *ClientRuntime) Context() context.Context {
	return r.ctx
}

// Cancel cancels the runtime context.
func (r *ClientRuntime) Cancel() {
	r.cancel()
}

// StartPProf starts the pprof server if an address is provided.
func (r *ClientRuntime) StartPProf(address string) {
	if address == "" {
		return
	}

	dlog.Client.Info("Starting PProf", address)
	go func() {
		if err := http.ListenAndServe(address, nil); err != nil {
			dlog.Client.Error("PProf server exited", err)
		}
	}()
}

// LogStartupMetrics logs startup profiling metrics when enabled.
func (r *ClientRuntime) LogStartupMetrics() {
	if r.profileEnabled {
		r.profiler.LogMetrics("startup")
	}
}

// LogShutdownMetrics logs shutdown profiling metrics when enabled.
func (r *ClientRuntime) LogShutdownMetrics() {
	if r.profileEnabled {
		r.profiler.LogMetrics("shutdown")
	}
}

// Stop stops profiling and logging runtime goroutines.
func (r *ClientRuntime) Stop() {
	r.profiler.Stop()
	r.cancel()
	r.wg.Wait()
}
