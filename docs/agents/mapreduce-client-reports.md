# MapReduce Client Reports: outfile append, report timing, logging under locks

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

- **`outfile append`** (`MaprClient.writeCumulativeResult`): a cumulative
  query (`dmap`, `dtail --query` with an outfile, see `isCumulative`)
  appends no rows on interim reports, as each holds the whole result so
  far; they only open the outfile for appending (`checkAppendOutfile`,
  creating it empty) so an unwritable path is logged every interval. The
  result lives only in memory until its final result is appended, once (`finish`, `dropQuery`, or
  `appendReplacedResult` when a reload commits another query; a failed
  commit appends nothing). Continuous jobs (`NonCumulativeMode`) append each
  interval's swapped-out rows once; scheduled jobs have no interim reports.
  Semantics per mode: `doc/querylanguage.md`.
- **Interim report timing** (`MaprClient.reportGeneration`): a query
  generation's first report is due half an interval after it started, but
  each server's first aggregates merged for it
  (`SessionSnapshot.AggregatesArrived`, signaled by `Aggregate.merge` once
  per server and generation) move it: once every live server
  (`MaprClient.allLiveArrived`: the `Server()` of each connection whose
  handler is not done, matched by name with
  `SessionSnapshot.ArrivedFrom`, so one that sent and left does not count
  for a missing one; liveness sampled at each arrival and, while servers
  are missing, every `firstReportSettle`, `firstReportWindow.postpone`, so
  a missing server whose connection ends no longer holds the table until
  the cap) sent its first
  aggregates, to
  `firstReportSettle` (1 s) after the last of them; while some are
  missing it waits for them, at most half an interval after the first
  server's (servers trailing by seconds, e.g. slower hosts or sequential
  session updates, were seen live; a server with no rows to send only
  costs that wait), and every wait capped at half the interval (`firstReportWindow`,
  `maprclient_report.go`; at `interval 1` a 1 s wait would land on the
  next sends and mix two intervals in one table). A due report that finds
  an arrival signaled but not yet taken (timer and arrival ready together)
  takes it as an arrival and is put off by the settle time (`postpone`);
  reporting at once put the later reports on the servers' send instants.
  The later reports are planned once per interval from the instant the
  previous one was planned for (`reportSchedule`, `nextReportDue`), not
  from its end, so the time reports take does not add up; that normally
  gives each table one send of every server, which is no guarantee, as the
  servers send by their own clocks. dservers send their first aggregates one interval after the
  start, so the first table used to appear only after 1.5 intervals. Continuous jobs (`NonCumulativeMode`) keep the fixed
  cadence; scheduled jobs have no interim reports.
- **Logging under the MapReduce state's locks** (`deferredLogger`,
  `internal/clients/maprclient_log.go`): the client's `SessionState`, its
  group sets and what the reporter logs inside
  `SessionState.WithCurrentSnapshot` (`MaprClient.lockedLogger`) log through
  a logger that queues (at most 256 diagnostics, the rest dropped and
  counted in one WARN) and never blocks; a goroutine that runs only while
  something is queued passes them on in order, and `MaprClient.finish`
  flushes it. The client logger blocks while the output is paused, and the
  one pause that can coincide with a `:reload` is the host key question of a
  server still in its handshake, which only the control loop answers: the
  reporter then waited in the paused logger (`Writing to outfile`) with the
  session state's read lock and the global group set's lock held, the
  reload's commit and end (`CommitQuery`, `Switching(false)`) waited for
  those locks, bounded by none of the 10 s caps, and the question was never
  answered (`TestMaprReportUnderPausedLoggerDoesNotBlockReload`).
  The control loop itself must not wait for the paused logger either, lock
  or not. Covered: what `commitSessionSpec` prints and logs on it goes
  through the same logger (`MaprClient.controlLogger`), i.e. the last table
  of a query that `:tail` or a reload ends (`dropQuery` ->
  `reportResultsTo`: rendered at once, its `Raw` payload queued and never
  dropped, printed in order once the logger runs again, so it can come
  after followed lines that were printed directly meanwhile), the errors
  of writing it, and of appending a replaced query's result
  (`appendReplacedResult`); and the aggregation failures of what a
  connection held through the reload, which `Aggregate.endHold` logs to
  the session state's logger instead of the connection's
  (`TestMaprTailSwitchUnderPausedLoggerDoesNotBlockControlLoop`,
  `TestEndHoldLogsFailuresToTheSessionLogger`). Not deferred: the periodic
  reporter and the final result of `finish` (synchronous, on goroutines
  that hold no lock while printing; `finish` flushes the queue first),
  `client.Aggregate`'s failures on the frame-reading goroutines (logged
  after releasing its lock, `withLock`), and trace diagnostics. The reload
  code outside the MapReduce client (`internal/clients/interactive_*.go`)
  answers on the control TTY, not through the client logger.
