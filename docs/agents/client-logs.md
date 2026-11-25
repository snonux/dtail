# Client Log Contents: Diagnostics vs Payload

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

The default client logger is `fout` (stdout + a daily file at
`<LogDir>/YYYYMMDD.log`, `LogDir` defaults to `~/log`). The logger and log
directory resolve as: explicit `--logger`/`--logDir` flag > `Common.Logger`/
`Common.LogDir` from the config file > per-command default (clients `fout` and
`~/log`, dserver `file` and `log`). The flags default to empty so an unset flag
never masks the config file. dtailhealth is the exception: it has no `--cfg`
flag and reads the user's client config, so it ignores `Common.Logger` and
`Common.LogDir` and uses `none` and `log` unless its own `--logger` flag is
given (it has no `--logDir` flag). By default the
`fout` daily file records DIAGNOSTICS ONLY — the small connection/audit lines (INFO/WARN/ERROR).
The full retrieved PAYLOAD (the bulk `dcat`/`dgrep`/`dtail` output) is NOT written
to the file by default, so a large read no longer silently grows the daily log by
the full payload size. Payload always still goes to STDOUT/terminal, unchanged.

The `fdata` logger (`loggers.newFdata`) is `fout` with the diagnostics in
the file only: stdout carries nothing but payload. It is the default of a
client with `--control json` (`config.ControlLogger`, set by
`cli.normalizeControl` unless `--logger` is given; see "JSON control
channel").

**Restoring the legacy full-payload tee (opt-in):**
- Client flag: `--log-payload` (all client commands: dcat, dgrep, dtail, dmap)
- Client config: `Client.LogPayload` (default `false`)
- When enabled, retrieved payload is teed into the daily log file as before.

**Seam and scope:**
- The split happens in the `fout` logger: diagnostics arrive via `Log`/`LogWithColors`
  (always written to both stdout and file); payload arrives via `Raw`/`RawWithColors`
  (always to stdout, to the file only when `LogPayload` is set).
- Only the default `fout` logger is affected. `--logger stdout` (no file sink) and
  `--logger none` are unaffected. `--logger file` is a pure file sink with no stdout
  tee; it deliberately still writes payload to the file, because otherwise its output
  file would be empty — payload teeing there is not gated by `--log-payload`.
- Note: the serverless direct-output path can bypass the logger and write payload
  directly to stdout; that path never wrote payload to the file and is unaffected.
  The disk-fill footgun lives on the `fout` file path (the server-mode receive
  path), which is what this setting gates. As that path also bypasses the
  logger's color switch, the client runtime passes the color decision itself
  (`clientRuntimeBoundary.serverlessColorizer` in
  `internal/clients/runtime_boundary.go`): with `--noColor` (or
  `Client.TermColorsEnable` false) serverless lines are uncolored
  `REMOTE|...` lines, byte-identical to server mode with `--noColor`; with
  `--plain` they are the raw lines, as in server mode with `--plain`.
- Serverless mode is a property of the runtime that built a session handler, not
  of the session: it holds exactly when the in-process client runtime hands over
  an output of its own (`handlers.Dependencies.ServerlessOutput`, the single
  source; there is no separate flag, and no stdout default). dserver never hands
  one over and ignores the `serverless=true` option a client sends, so a remote
  session can neither route its payload into the dserver process output instead
  of its SSH channel, nor read dserver's own stdin with the target `-`, which
  takes `readPipe` and so is checked by no `readfiles` permission.
- Daily file rotation uses a day name cached by the file sink and refreshed on its
  100 ms flush tick while active. Idle loggers stop their flush timers; the first
  file write after idle refreshes the day, without reading the clock per bulk
  payload line. Stdout arms its flush timer when buffered output first arrives;
  both sinks retain their original 100 ms tick schedule when rearming after idle.
  The file is chosen at
  write time: around midnight a line (including a timestamped dserver diagnostic)
  can land in the neighbouring day's file. This is intended.
  File sinks copy payload and diagnostics into owned 64 KiB batches rather
  than allocate/enqueue a message per line. Four queued batches, one producer
  batch and one worker batch bound payload backing memory to 384 KiB (plus the
  64 KiB output buffer). Buffers are reused locally; an unused sink allocates
  no batch storage. Partial batches flush on the existing 100 ms phase or on
  explicit Flush/shutdown. Calls are serialized in file-sink admission order;
  even a call larger than the queue is contiguous and keeps one destination
  through rotation. Shutdown drains accepted calls, including blocked large
  calls, and rejects later admissions with a diagnostic. A blocked sink still
  applies backpressure. The default payload-disabled fout path is unchanged.
