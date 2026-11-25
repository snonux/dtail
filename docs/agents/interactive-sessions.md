# Interactive Sessions: compatibility, acks and reload semantics

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

**Compatibility and session semantics:**
- The interactive bootstrap is always `SESSION START` (every protocol 5
  dserver supports it; a peer of another protocol version is rejected, see
  "Wire Framing"). A rejected or unacknowledged `SESSION START` fails the
  connection (exit status 1 for `dcat` and `dgrep`; `dtail` follow mode and
  `dmap` keep their statuses, see "Client Exit Statuses"); there is no
  fallback to the command stream
  (`dispatchInitialCommands` in
  `internal/clients/connectors/sessiontransport.go`).
- A `SESSION START`/`UPDATE` acknowledgement is waited for for 2 s of idle
  handler time (`defaultSessionAckTimeout`; a busy handler stretches it),
  counting only time the client handler was free to read frames
  (`baseHandler.WaitForSessionAck`, `busyClock` in
  `internal/clients/handlers/busyclock.go`): a payload write blocked by the
  paused output (or a host key prompt, a stalled pipe) can hold back an ack
  that arrived behind payload frames already on their way, so pausing right
  after the start no longer fails it. dserver sends every ack (ok or error)
  ahead of queued payload as soon as no frame is left half sent, and an ok
  ack ahead of all output of the workload it acknowledges (`sessionAckGate`,
  `internal/handlers/session_ack_gate.go`): before the dispatch the new
  generation is held, its payload stays queued and its server and MapReduce
  messages and a flush error of its read batch
  (`outputCoordinator.takeFlushError`) are deferred until the ack, which has a queue of its own
  (`sessionAcks`), went out; a workload that fails to start releases its
  hold on the input goroutine. The rest of a replaced generation is stale
  and dropped. So a slow link no longer makes an ack wait behind the
  session output buffer, also not the error ack of a rejected update while
  the running workload floods it; failure reports of the workload follow
  the ack. Non-`SESSION` command streams hold nothing, and payload still
  goes before other server messages. The initial `SESSION START`
  (connection goroutine) has no wall-clock cap; the session's end ends its wait, and so does the
  end of the connection's context (`:quit`, Ctrl-C;
  `baseHandler.WaitForSessionAckContext`, without which a quit during an
  unacknowledged start took 2 s of idle handler time). Everything the
  interactive control loop waits for is capped at 10 s wall clock
  (`sessionUpdateAckMaxWait` in `sessiontransport.go`): a `SESSION UPDATE`
  ack (`:reload`, `:query`, `:tail`, rollbacks) and the session lock a
  start in flight holds (`ReplaceStartSessionSpec`, which then returns
  `ErrSessionStartPending` and fails the reload), as the control loop alone
  answers a host key prompt whose output pause could otherwise deadlock it.
  Connections are updated and rolled back sequentially, so a failed reload
  with every server stalling takes up to about 20 s per connection
  (documented bound; no concurrent apply or total budget, which would break
  the all-or-nothing rollback). `applyLock.Unlock` of an unlocked lock
  panics like `sync.Mutex`. The rollback of an update that was sent but not
  acknowledged is numbered above it (base+2, `reloadRun.update`), and a
  wait skips ok acks of updates numbered below its own
  (`sessionRequest.awaitAck`), so a late ack of the timed-out update is not
  taken for the rollback's. Error acks carry no generation: a late
  rejection fails the request waiting then.
- A live `:reload` updates every active connection (session committed, its
  handler not done; `partitionReloadConnections` in
  `internal/clients/interactive_reload_inactive.go`) with `SESSION UPDATE`,
  all or nothing (a failure rolls the updated ones back). The only
  capability it checks up front, on the active connections, is `journal-v1`,
  for a reload to a `journal:` target (`checkReloadJournalCapability` in
  `internal/clients/interactive_control.go`): with one of them lacking it the
  reload is rejected on the TTY as a whole while the current workload keeps
  running. Connections without an active session (a server down, a session
  that ended) no longer fail the reload; `reloadRun.updateInactive` skips
  them and the TTY names them, a connection still connecting gets the
  reloaded spec for its SESSION START (`Connector.ReplaceStartSessionSpec`,
  under the connection's `applyMu`; a rollback gives the previous one back
  (`RestoreStartSessionSpec`; if that start already began with the
  reloaded spec and is still unacknowledged after 10 s, the rollback is
  deferred, `ErrSessionRollbackDeferred`: the start sends a `SESSION
  UPDATE` back to the previous spec right after its ack, under `applyMu`,
  and a failure of it fails the connection, `startInitialSession`; until
  that update finished `CommittedSession` reports the session as not
  committed, so a reload meanwhile takes the connecting path and waits for
  `applyMu` instead of recording the failed reload's spec for its own
  rollback; lines of the reloaded spec until the update's ack reach the
  client, as the client filters no output by generation, while its
  aggregates, which arrive outside the reload's switch and so are not
  held, are dropped up to that ack: the connection tells its handler
  before it defers the rollback, `Aggregate.AbandonStartWorkload`);
  once the start began, a committed one is updated like an active one, on
  the same base generation and all or nothing). Every connection is changed
  before the profile commit (for dmap, `CommitQuery`), so no connection
  sends results of the replaced query into the new generation. The other
  way round, a server starts the reloaded query right after its ack, so its
  first aggregates (a small file's whole result) can arrive before the
  commit: during a reload (`clientProfile.reloading`,
  `SessionState.Switching`) a connection's `Aggregate` merges what was
  queued before the ack frame into the current result and holds what
  follows (`Aggregate.SessionAcked`, called by `MaprHandler` in frame
  order) until the reload ended; a second ack within the reload (the
  rollback's) drops what was held. The reload's end
  (`Switching(false)`) merges what every connection holds into the result
  current by then itself, one holder after the other, each waiting for the
  group set's lock (`Aggregate.endHold`), and returns only then; a session
  that ended during the reload keeps its held messages through its
  shutdown `Flush`, and `MaprClient.finish` waits for a reload in flight
  (`awaitReload`), so the final result has them. After a failed reload,
  what a connection holds without a second ack (its session ended, or its
  rollback failed) is the abandoned query's and is dropped instead
  (`Aggregate.settleHold`, `SessionState.switchOutcome`). The hold is not
  capped: it is bounded by what a server sends during one reload, which
  the client keeps in memory anyway once merged (dropping beyond 4 MiB
  lost most rows of a large result). `Aggregate` logs nothing under its
  lock (`withLock`), for which the reload's end waits. A `SESSION START` ack during the reload holds too (a connecting
  server starts with the reloaded spec). Reloads number their updates on
  top of the highest generation committed or ever sent by a reload or
  rollback (`baseClient.reloadBase`), so unacknowledged numbers are not
  reused. Before, those
  aggregates went into the replaced query's result, or were dropped when
  `dtail` switched from following lines, and were missing in the new
  query's. With no
  active connection the reload is rejected (`no server has an active
  session`). A reload holds `baseClient.reloadMu` throughout and a `dtail`
  reconnect takes it to create and install its connection
  (`recreateConnection`), so a reconnect gets the spec as of before or after
  a reload, never one the reload left out. `:show` prints the number of
  servers (`servers=n`) and of active sessions (`sessions=n`).
- Successful reloads reuse the existing SSH session and advance a generation
  boundary so stale output from older workloads is dropped. dserver numbers
  the workloads of a connection itself (`sessionCommandState.generation`,
  never reused) and only acknowledges the client's generation number, so an
  update numbered like the running or an earlier workload still makes the
  replaced workload's queued output stale. A reload updates every connection
  on top of the highest committed generation of all of them
  (`applyReloadSpec`), so one that reconnected (and started over at 1) does
  not make later reloads fail with mismatched generations.
- A connection takes one session channel and a channel one shell request;
  further ones are refused (`internal/server/server.go`), so `MaxConnections`
  bounds the sessions.
- Interactive `dtail` (with or without `--query`) is built by
  `clients.NewInteractiveTailClient` (`cmd/dtail/main.go`
  `buildTailClient`): a `MaprClient` in tail mode whose query is optional
  (`clientProfile.switchWorkload`), with `MaprHandler`s, which print payload
  and aggregate the aggregate frames. Its running session switches between
  following lines and a query with `SESSION UPDATE`
  (`baseClient.switchToQuery`/`switchToTail` in
  `internal/clients/interactive_workload.go`, on top of
  `applyInteractiveReload`; driven by the `:query`/`:tail`
  commands). dserver and the serverless runtime need no change for it
  (`TestSessionUpdateSwitchesTailToQueryAndBack` in
  `internal/handlers/session_modeswap_test.go`). The query reads without the
  tail's regex, `--invert`, context and `--max` (`tailFilter`, set aside and
  restored on the way back, also for a `--query` given at start; an explicit
  regex for the way back clears `--invert`). `MaprClient.commitSessionSpec`
  takes a spec without query only for this client: `dropQuery` reports the
  result once more (`reportResults(true)`: with an outfile it is always
  written; without one `printResults` prints it only if the rendered table
  has rows and changed, and for `dtail` without outfile, non-cumulative, it
  covers only what arrived since the last report; dserver aborts the
  replaced aggregate, so up to one interval is missing), then `SessionState.ClearQuery` (no
  query, no global group set; `Aggregate.merge` drops aggregates silently
  while there is none). `MaprHandler` takes payload frames for aggregate
  data only while `SessionState.AggregatesExpected` (a committed query, or
  a pending switch to one, set by `clientProfile.expectQuery` before the
  update and reset on failure), so followed `--plain` lines starting with
  `AGGREGATE|` are printed; frames are otherwise handled as by a
  `ClientHandler`. The reporter idles without a query
  (`awaitQueryChange`). `finish` reports nothing while the session follows lines, and
  the incomplete-result warnings skip failures reported before the switch
  to the query (`queryFailureMarks`). Exit statuses are those of dtail's
  follow mode; non-interactive `dtail`, `dmap`, `dcat`, `dgrep` and the
  jobs are unchanged.
