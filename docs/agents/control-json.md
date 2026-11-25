# JSON Control Channel (--control json)

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

`--control json` (`Args.Control`, `config.ControlJSON`) is the interactive
session for programs and LLM agents: one JSON request per line, exactly one
JSON response line per request, events in between. User doc:
`doc/agent-control.md`. Code: `internal/clients/control_json.go` (types,
`controlWriter`, the loop), `control_json_commands.go` (command table,
reload/query/tail/show/pause), `control_json_probe.go` (fields, probe, list,
complete), `control_json_results.go` (results, the dmap query run).

- **Flags** (`cli.BindCommonClientFlags`, all clients): `--control json`,
  `--control-in PATH` (default stdin), `--control-out PATH` (default
  stderr). `cli.normalizeControl` (in `prepareClient`, before the setup)
  rejects another `--control` value and `--control-in`/`--control-out`
  without it, sets `InteractiveQuery` and, unless `--logger` is given, the
  logger `config.ControlLogger` (`fdata`), ahead of `Common.Logger` of the
  config file. `InteractiveDefault.Resolve` and `ResolveQueryBuilder` put
  the control channel before `--no-interactive` and the terminal rules:
  dtail is interactive, dmap uses `QueryBuilderClient` exactly when it has
  no query.
- **`fdata` logger** (`loggers.newFdata`, `fout.fileOnlyDiagnostics`):
  payload to stdout, diagnostics to the daily file only, so a program's
  data holds no log lines. Listed in `examples/dtail.schema.json`.
- **Start** (`baseClient.startJSONControl`, called by
  `startInteractiveControl`): no control TTY, no line editor.
  `openControlChannel` opens a FIFO input O_RDWR, so it does not end when a
  writer of one request closes it (`TestControlInputFIFOSurvivesItsWriters`);
  the output path is appended to. The host key question gets an empty input
  (a no): stdin may be the requests. The prober, the connections and, for
  dmap's held sessions, the keepalive run as in every interactive session.
- **Loop** (`runJSONControl`, `controlSession.handleLine`): `ready` event
  (`controlProtocol`, mode, workload, files, servers, commands), then one
  request at a time (`parseControlRequest`: unknown fields are an error, so
  a misspelt field does not run the command without it). Lines are read on a goroutine (`readControlLines`,
  at most `controlMaxLine` = 1 MiB each). `quit` or the end of the input
  ends the session; a failed write of a response ends the loop. The
  `controlWriter` exists from `newBaseClient` on and drops lines until
  `open`, so the result reporter can hold it.
- **Commands** (`controlCommands`, one table of name, help text,
  availability and handler; `controlCommandNames` for `ready` and error
  texts): they call what the TTY commands call. `reload` passes `args` to
  `parseInteractiveReloadArgs` (no tokenizer, so no quoting rules);
  `query`/`tail` go through `parseQueryCommand`/`parseTailCommand`;
  `complete` calls `completeEditorLine`, so it waits for a path listing or
  table probe like Tab; `fields` waits for a probe (`controlProbeWait`,
  5 s) instead of answering "not yet"; `list` reads the catalog's listing
  cache and fetches what is missing. Commands whose answer a server sends
  (`resumes`) resume a paused output first, the waits of the others hold
  it (`controlSession.waitHeld`, see "Tab waits hold the output").
- **dmap without a query** (`QueryBuilderClient`, `profile.runQuery`): the
  sessions are held as the query builder holds them (at most 3, see "dmap
  Query Builder"), and each `query` request runs `NewMaprClient` on
  connections of its own (`runQuery`: not interactive, `Quiet`, result sink
  and progress sink set) and answers with `controlQueryRun`: the final
  result, the run's exit status, `warnings` (`resultWarnings`:
  `incompleteResultReasons` plus file read failures) and `read` (the read
  progress totals at the end). The reason is the builder's: only a plain
  command stream ends with the close handshake that tells a result is
  complete. Requests are sequential, so a running query blocks the next
  request; it ends with the session's context (SIGINT, SIGTERM).
- **Results** (`MaprClient.resultSink`, `sinkResults`): with a sink the
  table is not printed. `mapr.GroupSet.ResultTable` gives columns and rows;
  `newControlResult` cuts the rows to the query's `limit`, else to
  `controlResultMaxRows` (200), keeping `rowsTotal`. Interim results are
  passed on only with rows and when changed (`CommitRenderedResult`), the
  final one always. A query with an outfile writes it as before and the
  sink gets its path. Sessions with a workload (`newMaprClient` with
  `isJSONControl`) emit `result` events (`emitControlResult`).
- **Progress events**: `stats.progressSink` gets every changed progress of
  a run (`tickProgress`, 1 s); `runHeldQuery` writes them with the request's
  id (`controlProgress`, percent with one decimal).
- Tests: `internal/clients/control_json_test.go`,
  `control_json_fifo_linux_test.go`, `internal/cli/control_test.go`,
  `TestFdataKeepsDiagnosticsOffStdout`, `TestGroupSetResultTable`,
  `integrationtests/control_json_test.go` (`TestDMapControlJSON`,
  `TestDTailControlJSON`, dserver and serverless, over pipes).
