package cli

import (
	"context"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/profiling"
	"github.com/mimecast/dtail/internal/source"
)

// ClientRuntime owns common client command runtime components.
type ClientRuntime struct {
	ctx            context.Context
	cancel         context.CancelFunc
	loggerCancel   context.CancelFunc
	wg             sync.WaitGroup
	pprofServer    *PProfServer
	profiler       *profiling.Profiler
	profileEnabled bool
}

// NewClientRuntime starts logging and profiling for a client command.
func NewClientRuntime(parent context.Context, profileFlags profiling.Flags, profileName string) *ClientRuntime {
	return newClientRuntime(parent, profileFlags, profileName, dlog.Start)
}

type clientLoggerStarter func(context.Context, *sync.WaitGroup, source.Source)

func newClientRuntime(parent context.Context, profileFlags profiling.Flags, profileName string,
	startLogger clientLoggerStarter) *ClientRuntime {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	// The work context may be canceled by a timeout or signal before clients
	// finish their teardown reporting. Keep the logger alive until Stop so
	// MaprClient's final aggregate and shutdown diagnostics can still be queued
	// and flushed by every logger implementation.
	loggerCtx, loggerCancel := context.WithCancel(context.WithoutCancel(parent))
	runtime := &ClientRuntime{
		ctx:            ctx,
		cancel:         cancel,
		loggerCancel:   loggerCancel,
		profiler:       profiling.NewProfiler(profileFlags.ToConfig(profileName)),
		profileEnabled: profileFlags.Enabled(),
	}

	runtime.wg.Add(1)
	startLogger(loggerCtx, &runtime.wg, source.Client)
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

	r.stopPProf()

	server, err := NewPProfServer(address)
	if err != nil {
		dlog.Client.Error("Unable to start PProf", err)
		return
	}

	r.pprofServer = server
	dlog.Client.Info("Starting PProf", server.Address())
	server.Start(&r.wg)
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
	r.stopPProf()
	r.cancel()
	if r.loggerCancel != nil {
		r.loggerCancel()
	}
	r.wg.Wait()
}

func (r *ClientRuntime) stopPProf() {
	if r.pprofServer == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.pprofServer.Shutdown(ctx); err != nil {
		dlog.Client.Error("Unable to stop PProf", err)
	}
	r.pprofServer = nil
}
