package handlers

import (
	"context"
	"sync/atomic"

	"github.com/mimecast/dtail/internal/protocol"
)

// mapQueryFailures remembers the command stream whose map command failed,
// e.g. an older client's query that this dserver no longer accepts (such as
// one selecting count(x) twice). A client sends the map command before its
// read commands; without an aggregate those reads would stream every line as
// plain payload to a client waiting for MapReduce results (an older dmap
// writes a header-only outfile and exits 0, an older dtail --query or
// continuous job streams for ever). So every later read of that stream is
// rejected instead, and the session ends with its normal close handshake once
// no command is left.
//
// A command stream is one session generation: generation 0 for the legacy
// command stream of a connection, and each SESSION START or UPDATE generation
// of an interactive session. A failure of one generation therefore does not
// affect the reads of another, e.g. of a later successful :reload. A map
// command counts as failed when it can not be built (handleMapCommand) and
// when dispatchCommand rejects it before its handler runs (commandRejected).
// A failure is cleared when a map command of its generation succeeds and when
// a SESSION START or UPDATE (re)uses its generation: a retried SESSION START
// reuses generation 1, and the client may number an UPDATE as an earlier,
// rejected one, possibly without a query. Sessions whose map command
// succeeded, and sessions without one, are not affected.
type mapQueryFailures struct {
	// generation holds the failed generation plus one, so 0 means none. Only
	// the last failure is kept: a client starts a new generation only after
	// the previous one's commands were dispatched.
	generation atomic.Uint64
}

// mark records that the map command of generation failed.
func (f *mapQueryFailures) mark(generation uint64) {
	f.generation.Store(generation + 1)
}

// clear forgets a failure of generation once a map command of it succeeded.
func (f *mapQueryFailures) clear(generation uint64) {
	f.generation.CompareAndSwap(generation+1, 0)
}

// failed reports whether a map command of generation failed.
func (f *mapQueryFailures) failed(generation uint64) bool {
	return f.generation.Load() == generation+1
}

// rejectAfterMapQueryFailure tells the client that this read does not run
// because the map command of its command stream failed. It reports the
// failure with the same fixed reason as the map command, which clients
// de-duplicate and the scheduler counts as a transport failure, so a
// scheduled job writes no outfile (see protocol.IsFileReadFailure).
func (r *readCommand) rejectAfterMapQueryFailure(ctx context.Context, args []string) {
	file := ""
	if len(args) > 1 {
		file = args[1]
	}
	r.sendServerMessage(ctx, r.logger.Error(r.logContext,
		"Not reading, the session's map query failed", file))
	r.reportFailure(ctx, protocol.CommandFailureMapQuery)
}
