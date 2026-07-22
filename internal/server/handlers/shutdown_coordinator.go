package handlers

import (
	"context"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
)

// aggregateInputGrace is how long the coordinator waits after observing
// pendingFiles==0 before re-checking and signaling input-exhausted to the
// output aggregate. It mirrors the 100ms channel-registration grace of the
// non-output aggregate (see Aggregate.nextLine): a sibling read command from
// the same client batch may already be dispatched (activeCommands counted)
// but not yet registered its files via AddPendingFiles.
const aggregateInputGrace = 100 * time.Millisecond

type shutdownCoordinator struct {
	server readCommandServer
	// oneShotInput is true for cat/grep style reads whose input is exhausted
	// once every file has been read to EOF. Follow-mode (tail) reads never
	// exhaust their input, so they must never finish the output aggregate.
	oneShotInput bool
}

func newShutdownCoordinator(server readCommandServer, oneShotInput bool) *shutdownCoordinator {
	return &shutdownCoordinator{server: server, oneShotInput: oneShotInput}
}

func (c *shutdownCoordinator) onFileProcessed(path string) {
	remaining, activeCommands := c.server.CompletePendingFile()
	dlog.Server.Debug(c.server.LogContext(), "File processing complete", "path", path, "remainingPending", remaining)

	if remaining != 0 {
		return
	}

	// All pending file reads are drained: for one-shot inputs let a output
	// aggregate finish so a blocked server-mode map command can return (see
	// maybeFinishAggregateInput for the circular wait this prevents).
	c.maybeFinishAggregateInput()

	if activeCommands != 0 {
		return
	}

	c.finalizeWhenIdle()
}

// maybeFinishAggregateInput signals input-exhausted to the output
// aggregate once all pending one-shot file reads have drained. This is what
// terminates a server-mode output dmap: the map command blocks inside
// Aggregate.Start and keeps the session's active-command count above
// zero, while session shutdown in turn waits for that count to reach zero.
// Without this signal neither side can make progress and the client hangs
// forever even after receiving all results (the aggregate used to be
// finished only at session teardown or generation-replacement Abort).
//
// The aggregate pointer is captured before the grace sleep and compared
// afterwards, so an interactive :reload (which replaces the aggregate for a
// new generation) can never have its fresh aggregate finished by a stale
// pending==0 observation from the previous generation.
func (c *shutdownCoordinator) maybeFinishAggregateInput() {
	if !c.oneShotInput {
		return
	}
	aggregate := c.server.Aggregate()
	if aggregate == nil {
		return
	}

	// Grace period: a sibling read command of the same batch may be
	// dispatched but not yet registered in the pending-files counter;
	// finishing now would drop that command's contribution to the result.
	time.Sleep(aggregateInputGrace)
	if pending, _ := c.server.PendingAndActive(); pending != 0 {
		return
	}
	if c.server.Aggregate() != aggregate {
		return
	}
	aggregate.FinishInput()
}

func (c *shutdownCoordinator) finalizeWhenIdle() {
	// If we have a output aggregate, trigger final serialization.
	if aggregate := c.server.Aggregate(); aggregate != nil {
		dlog.Server.Info(c.server.LogContext(), "Triggering final output aggregate serialization")
		aggregate.Serialize(context.Background())
		// In serverless mode, serialization is synchronous, so no wait needed.
		if !c.server.Serverless() {
			time.Sleep(c.server.ShutdownSerializeWait())
		}
	}

	// Double-check that we really have no pending work before shutdown.
	if !c.server.Serverless() {
		time.Sleep(c.server.ShutdownIdleRecheckWait())
	}
	finalPending, finalActive := c.server.PendingAndActive()
	if finalPending == 0 && finalActive == 0 {
		dlog.Server.Debug(c.server.LogContext(), "No active commands and no pending files after double-check, triggering shutdown")
		c.server.TriggerShutdown()
		return
	}

	dlog.Server.Debug(c.server.LogContext(), "Shutdown check cancelled", "finalPending", finalPending, "finalActive", finalActive)
}
