package handlers

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
)

type shutdownCoordinator struct {
	server *ServerHandler
}

func newShutdownCoordinator(server *ServerHandler) *shutdownCoordinator {
	return &shutdownCoordinator{server: server}
}

func (c *shutdownCoordinator) onFileProcessed(path string) {
	remaining := atomic.AddInt32(&c.server.pendingFiles, -1)
	dlog.Server.Debug(c.server.user, "File processing complete", "path", path, "remainingPending", remaining)

	if remaining != 0 || atomic.LoadInt32(&c.server.activeCommands) != 0 {
		return
	}

	c.finalizeWhenIdle()
}

func (c *shutdownCoordinator) finalizeWhenIdle() {
	// If we have a turbo aggregate, trigger final serialization.
	if c.server.turboAggregate != nil {
		dlog.Server.Info(c.server.user, "Triggering final turbo aggregate serialization")
		c.server.turboAggregate.Serialize(context.Background())
		// In serverless mode, serialization is synchronous, so no wait needed.
		if !c.server.serverless {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Double-check that we really have no pending work before shutdown.
	if !c.server.serverless {
		time.Sleep(10 * time.Millisecond)
	}
	finalPending := atomic.LoadInt32(&c.server.pendingFiles)
	finalActive := atomic.LoadInt32(&c.server.activeCommands)
	if finalPending == 0 && finalActive == 0 {
		dlog.Server.Debug(c.server.user, "No active commands and no pending files after double-check, triggering shutdown")
		c.server.shutdown()
		return
	}

	dlog.Server.Debug(c.server.user, "Shutdown check cancelled", "finalPending", finalPending, "finalActive", finalActive)
}
