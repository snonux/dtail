# Output Path and MapReduce Operations

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

DTail uses a single, channel-less read/output path for both direct output
operations (cat, grep, tail) and MapReduce operations in server mode. This was
formerly called "turbo boost" mode and offered as an opt-out optimization; it is
now the one and only mode. There is no on/off toggle: the old
`DTAIL_TURBOBOOST_DISABLE` environment variable and the `Server.TurboBoostDisable`
config field have been removed. `DTAIL_TURBOBOOST_DISABLE` is now inert (a no-op),
and a leftover `TurboBoostDisable` key in an old config file is silently ignored
(config decoding does not reject unknown keys). The example schema
`examples/dtail.schema.json` does not list the removed `Turbo*` keys, so schema
validation flags them as stale, while the runtime still ignores them.

**Technical Details:**
- For cat/grep/tail: the read path writes directly to the output/connection
  without channel hand-offs.
- Without before-context (max/after-only context is eligible), an emitted file-backed
  cat/grep/tail line is handed to `DirectLineProcessor` as a borrowed slice of
  the reader's buffer through the optional `line.RawProcessor` interface, so no
  pooled per-line buffer is used; the slice is only valid during the call and
  must not be retained. Before-context greps, journal (`journal:`) reads and the
  MapReduce processor keep the owned-buffer `ProcessLine` path.
- For MapReduce in server mode: lines are processed directly without channels.
- For MapReduce in serverless/client mode: the server-side direct processing does
  not apply — client-side aggregation runs on the client.
- Client receive of aggregate messages (`internal/clients/handlers/maprhandler.go`,
  `internal/mapr/client/aggregate.go`): frames are cut with `bytes.IndexByte`,
  each message is validated and copied into a reused queue (`Aggregate.Add`),
  and at the end of every `Write` call the queue is aggregated, in arrival
  order and each message on its own, straight into the global group set under
  one non-blocking lock attempt (`GlobalGroupSet.UpdateNoblock`); a missed
  attempt keeps the queue, retries 10 ms later in the background
  (`Aggregate.retryMerge`, again without waiting for the lock; without it
  the rows waited for the connection's next aggregate messages, one
  interval later, as the servers send at the same moment) and at the next
  call (waiting for the lock only once 4 MiB are queued), and `Flush` on
  shutdown waits. The result therefore does
  not depend on how messages were batched or on lock contention.

**Server-Side MapReduce (dserver):**
- Lines are processed directly without channel overhead
- Batch processing reduces lock contention
- Memory pooling reduces garbage collection pressure
- Same output format and accuracy regardless of workload
- Results are sent as one aggregate frame per group, but a session output read
  frames every already queued group, up to the read buffer (32 KiB from the
  SSH copy), into one SSH channel write (`sessionFramer.readMaprMessage`,
  `outputCoordinator.tryReadQueuedMapr`; the per-session queue holds
  `maprMessageQueueSize` = 256 groups). It takes a group only where the next
  read would have taken it anyway (no payload, flush error or server message
  pending), filters each by its generation, and changes no byte of the stream.
  A 200k-group result went from about 149k write(2) calls to about 2.2-2.7k.
  The rest of a batch that does not fit into one read goes out with the
  groups queued behind it, not alone in a tiny write of its own.
  The queue is bounded by count only and lies outside `OutputBufferMaxBytes`:
  a group's key and values (e.g. `last()`) can each be up to `MaxLineLength`
  bytes, so a session can hold up to (256 + 16 + 1) x (1 + select fields) x
  `MaxLineLength` bytes of queued groups (the session queue, the map command's
  16-entry forwarding channel and the forwarder's message in hand). Typical
  groups are a few dozen bytes; the aggregate itself holds all groups anyway.
- The final serialization of a map command (`Aggregate.finalize` in
  `internal/mapr/aggregate/aggregate.go`) has no deadline of its own: a slow
  or paused client only slows it down through output backpressure, and only
  the session's context (session teardown, dserver shutdown, a replaced
  generation) or the connection's `IdleSessionTimeoutS` ends it early. It
  used to stop after a fixed 10 s, dropping the unsent groups while the
  session still closed normally (an incomplete outfile with exit status 0).
  The finalization records a three-way outcome: none (it never ran, e.g.
  an abort won because the command was canceled or the session generation
  was replaced, which has no final result), complete, or interrupted (the
  finalization context ended while it waited for the processors or part way
  through the sends). An interrupted final serialization drops the unsent
  groups instead of re-merging them, as nothing would send them any more,
  and logs one WARN (`Aggregate final serialization interrupted; final
  result incomplete`, with the number of unsent groups). As a defensive
  guard, the session's graceful shutdown then reports `.syn command failed
  map: incomplete result` (`protocol.CommandFailureMapIncomplete`,
  `Aggregate.FinalResultInterrupted`, `reportIncompleteMapResult` in
  `internal/handlers/basehandler.go`) before the close handshake, so a
  scheduled job writes no outfile. With the deadline gone, the context ends
  listed above tear the session down rather than close it gracefully, so a
  client normally sees a session cut short instead (a scheduled job then
  writes no outfile either); the report covers any path that still ends a
  final result early and then closes normally. Interactive `dmap` keeps its
  exit status (`0`) and writes its outfile in both cases, but warns about
  the reported failure as well as about the session cut short (see "Client
  Exit Statuses" below). Each final serialization logs `Aggregate final
  serialization started` with its group count at INFO; the count (like the
  unsent count of the WARN) excludes the groups a periodic serialization
  still in flight took for its own snapshot, which that pass sends or
  re-merges. `integrationtests/dmap_slow_client_test.go` (query with
  `interval 3600`, so no periodic pass runs) stops a client for 15 s once
  that line shows all 100,000 groups still to be sent, and requires every
  group in the outfile and no interrupted serialization in the server log.

**Output limits and deadlines (server config):**
The output path exposes four optional settings: `OutputBufferMaxBytes`,
`OutputFlushTimeoutMs`, `OutputReadRetryIntervalMs`, and
`OutputEOFAckTimeoutMs`. The buffer setting bounds retained payload memory; the
two timeout settings bound flush and EOF-ack waits. The flush timeout of the
payload buffer (`outputManager.flush`) is a stall timeout: it starts over
whenever the reader took something since it was last checked, so a client on a
slow link is waited for. It, the session flush (`flushContext`) and the 5 s
wait for the close acknowledgement also do not run out while the session
reader is still writing bytes it already took to the client
(`outputCoordinator.clientSlow`): a paused client (a pager, a stopped
pipeline) only applies backpressure, as during the read, and the connection's
idle timeout bounds one that never comes back. Only a reader that takes
nothing for a whole timeout without a pending delivery fails the flush.
`OutputReadRetryIntervalMs`
is a compatibility fallback for stale-generation writers that do not provide a
cancellation signal, and values below the implementation safety minimum are
clamped. Removed `Turbo*`, output-delay, EOF-wait-sizing, flush-poll, and
shutdown-wait keys in older config files are ignored by the lenient decoder.

Authenticated connections use the rolling `IdleSessionTimeoutS` timeout (default
900 seconds). Successful network reads or writes mark the connection active, and a
per-connection ticker (timeout/4, clamped to 1-30 seconds) pushes the deadline to
now + timeout + two intervals when activity was seen, so the hot I/O path does no
clock reads or `SetDeadline` calls. Active follow sessions stay connected: any idle
gap up to the timeout is still refreshed in time even when the refresh tick runs
less than one interval late. Clients with no SSH activity are closed between the
timeout plus two intervals and the timeout plus three intervals after their last
activity (about 960-990 seconds with the defaults; late ticks can only extend
this). The interval never drops below one second, so a timeout below four seconds
closes two to three seconds past it instead of proportionally.
Per-session payload backing memory waiting on a slow client is capped by
`OutputBufferMaxBytes` (default 2 MiB); producers apply backpressure when the cap is reached. The
configured cap must leave room for one maximum-length formatted line: it must be
at least `MaxLineLength + 128 KiB`. Every session escapes its frames (see Wire
Framing), so every client session (`ServerHandler`; a health session sends
only a few short frames and keeps the default) raises its own cap to at least `2*MaxLineLength + 128 KiB`
(`escapedOutputBufferMaxBytes`, `sessionOutputBufferMaxBytes` in
`internal/handlers/serverhandler.go`), since escaping 0xAC/0xFF bytes can
almost double a line frame; validation is unchanged, so every configuration
valid before keeps working, and memory stays bounded at about one more
maximum-length line per session. A batch of line
frames that still exceeds the cap is reported, not dropped silently: the client
gets a visible `Dropped output too large for the session output buffer` warning
and the failed-command reason `output: too large for the session output buffer`.

**Best Practices for High-Concurrency MapReduce:**
1. Increase MaxConcurrentCats in the server configuration to match workload
2. Use server mode for large-scale MapReduce operations
3. Monitor logs for performance metrics

Note: three operator-facing diagnostic log lines still contain the word "turbo"
verbatim ("Using turbo mode for reading", "Using turbo aggregate processor for
MapReduce", "Creating turbo aggregate for MapReduce"). These are deliberately kept
as stable log strings and do not imply a separate mode.
