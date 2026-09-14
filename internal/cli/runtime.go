package cli

import (
	"context"
	"fmt"
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
	profiler       clientProfiler
	profileEnabled bool
}

// NewClientRuntime starts logging and profiling for a client command.
func NewClientRuntime(parent context.Context, profileFlags profiling.Flags, profileName string) (*ClientRuntime, error) {
	return newClientRuntime(parent, profileFlags, profileName, dlog.Start)
}

type clientLoggerStarter func(context.Context, *sync.WaitGroup, source.Source) error
type clientProfiler interface {
	LogMetrics(string)
	Stop()
}
type clientProfilerFactory func(profiling.Config) clientProfiler

func newClientRuntime(parent context.Context, profileFlags profiling.Flags, profileName string,
	startLogger clientLoggerStarter) (*ClientRuntime, error) {
	return newClientRuntimeWithProfiler(parent, profileFlags, profileName, startLogger,
		func(cfg profiling.Config) clientProfiler { return profiling.NewProfiler(cfg) })
}

func newClientRuntimeWithProfiler(parent context.Context, profileFlags profiling.Flags, profileName string,
	startLogger clientLoggerStarter, newProfiler clientProfilerFactory) (*ClientRuntime, error) {
	if parent == nil {
		return nil, fmt.Errorf("create client runtime: context must not be nil")
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
		profiler:       newProfiler(profileFlags.ToConfig(profileName)),
		profileEnabled: profileFlags.Enabled(),
	}

	runtime.wg.Add(1)
	if err := startLogger(loggerCtx, &runtime.wg, source.Client); err != nil {
		runtime.wg.Done()
		runtime.profiler.Stop()
		cancel()
		loggerCancel()
		return nil, fmt.Errorf("start client logger: %w", err)
	}
	return runtime, nil
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

	server, err := NewPProfServer(r.ctx, address)
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

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), 5*time.Second)
	defer cancel()

	if err := r.pprofServer.Shutdown(ctx); err != nil {
		dlog.Client.Error("Unable to stop PProf", err)
	}
	r.pprofServer = nil
}
