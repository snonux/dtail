package handlers

import (
	"context"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
)

type shutdownCoordinator struct {
	server readCommandServer
}

func newShutdownCoordinator(server readCommandServer) *shutdownCoordinator {
	return &shutdownCoordinator{server: server}
}

func (c *shutdownCoordinator) onFileProcessed(path string) {
	remaining, activeCommands := c.server.CompletePendingFile()
	dlog.Server.Debug(c.server.LogContext(), "File processing complete", "path", path, "remainingPending", remaining)

	if remaining != 0 || activeCommands != 0 {
		return
	}

	c.finalizeWhenIdle()
}

func (c *shutdownCoordinator) finalizeWhenIdle() {
	// If we have a turbo aggregate, trigger final serialization.
	if turboAggregate := c.server.TurboAggregate(); turboAggregate != nil {
		dlog.Server.Info(c.server.LogContext(), "Triggering final turbo aggregate serialization")
		turboAggregate.Serialize(context.Background())
		// In serverless mode, serialization is synchronous, so no wait needed.
		if !c.server.Serverless() {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Double-check that we really have no pending work before shutdown.
	if !c.server.Serverless() {
		time.Sleep(10 * time.Millisecond)
	}
	finalPending, finalActive := c.server.PendingAndActive()
	if finalPending == 0 && finalActive == 0 {
		dlog.Server.Debug(c.server.LogContext(), "No active commands and no pending files after double-check, triggering shutdown")
		c.server.TriggerShutdown()
		return
	}

	dlog.Server.Debug(c.server.LogContext(), "Shutdown check cancelled", "finalPending", finalPending, "finalActive", finalActive)
}
