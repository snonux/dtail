# Read Progress (dcat, dgrep, dmap)

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

dserver reports how far the snapshot reads of a session got with the hidden
control frame `.syn progress bytes=<n> total=<n> files=<n> filestotal=<n>`
(`protocol.EncodeProgress`/`ParseProgress`, `internal/protocol/progress.go`;
key=value tokens, unknown keys ignored, missing ones 0). It is part of
protocol 5 without a capability: older protocol 5 clients drop unknown
hidden messages, and a client of an older dserver simply gets none. Design:
`docs/dev/read_progress.md`.
- **Counted:** cat and grep mode reads of files, so dcat, dgrep and dmap's
  reads; not tail mode, `journal:` targets, stdin or the scheduler's group
  reads. `readCommand.trackProgress` registers a file once its permission
  check passed, with its `os.Stat` size, so the total of a read command is
  complete at once also for files still waiting for a cat slot; every file
  argument is a read command of its own, so a session's total grows as its
  commands start and the percentage can fall when one does (`readProgress`,
  `fileProgress`, `internal/handlers/read_progress.go`). Bytes are counted
  by `fs.ReadOptions.Progress` (`fs.ByteCounter`): `countedSource`
  (`internal/io/fs/progress.go`) wraps the open descriptor below bufio and
  the gzip/zstd decompressor, one atomic add per buffer fill; compressed
  files count compressed bytes on disk; a reopen starts the file's count
  over.
- **Growing files:** every report tick re-stats the files being read through
  their open descriptor (`fileProgress.refreshSize`; rotation-safe, at most
  `MaxConcurrentCats` per session), so the total follows a log that is
  appended to while it is read; a file read past its known size counts with
  the bytes read as its size. A file whose read ended (EOF, `--max`,
  cancel, error) counts with the bytes read as its size too
  (`fileProgress.finish`): the rest of a file whose read ended early leaves
  the total, so the final report is 100% and `bytes` is what was read.
- **Generations:** a read command of a newer session generation starts the
  tracking over; older ones are not tracked. Plain command streams are
  generation 0 throughout and add up.
- **Sending:** a goroutine per session posts every second
  (`progressReportInterval`) when the totals changed, ends once every
  registered file's read ended (`flushAndIdle`; the next registration
  starts it again), and
  `readCommand.readFiles` posts once its files ended. Posts go to a one-slot
  mailbox (`outputCoordinator.postProgress`, `output_progress.go`) that
  replaces an unsent frame, is taken **ahead of payload** at a frame
  boundary (`tryReadProgress` in `next`; otherwise a cat keeping the output
  buffer full starved it), before the close handshake (`tryReadQueued`,
  counted by `queuedUnits`), not while `sessionAckGate.pending()`, and is
  dropped for a replaced generation. Serverless uses the same path.
- **Client:** `baseHandler.handleProgressMessage` keeps the latest
  (`ProgressReporter`, `internal/clients/handlers/progress.go`);
  `baseClient.progressTotals` sums all connections and counts `reporting`
  servers; `progressMeter` (`internal/clients/progress.go`) measures the
  rate over 15 s (started over when the bytes shrink, not when only the
  total does) and the ETA. The stats loop measures it every second
  (`progressTick`, `stats.tickProgress`); its STATS tick (every 3 s,
  `statsTicks`, `stats.tick`) adds `bar` (20 ASCII cells, `#` per 5%, `?`
  without a report), `progress%`, `progress`, `rate`, `eta`, `files`,
  `reporting`, `bytes`, `total` (the order of a PROGRESS line, bar first so
  successive lines show it filling up) to the STATS line when
  the progress changed since the last STATS line (so STATS lines now also
  appear while reads progress; dropped by `--quiet`/`--plain`). STATS
  fields are sorted by name (`protocol.EncodeStatsLine`, `DLog.Mapreduce`,
  which also sorts dserver's `MAPREDUCE:STATS` fields).
  `--progress` (`Args.Progress`) writes fixed-order `PROGRESS|...` lines to
  stderr every second the progress changed, whatever the quiet mode, and a final one after the sessions ended
  (`stats.finishProgress`, from `baseClient.Start`; not written when the
  last line already showed the final numbers); not in interactive
  sessions or the dmap query builder. Without any report, `progress%`,
  `rate` and `eta` are `unknown` (final line `reporting=0/N`, also when a
  session read no file: none matched or none was permitted).
- **Replaced workloads:** an acknowledged `SESSION START`/`UPDATE` makes
  the client handler forget the progress (`baseHandler.resetProgress`, from
  `handleSessionAckMessage`): dserver sends the new workload's progress only
  after its ack and drops frames of the replaced one, so a workload that
  reports none (following lines, a journal) no longer shows the numbers of
  the reads before it in the Ctrl-C statistics.
- Tests: `read_progress_test.go` (handlers), `progress_test.go` (protocol,
  fs, clients, client handlers), `integrationtests/progress_test.go`.
