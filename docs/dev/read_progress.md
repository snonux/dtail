# Read progress (`.syn progress`)

`dcat`, `dgrep` and `dmap` can read many gigabytes per server. dserver tells
the client how far the reads of a session got, and the client shows bytes,
total, percentage, rate and an ETA summed over all servers.

## The frame

```
.syn progress bytes=<n> total=<n> files=<n> filestotal=<n>
```

It is a hidden control message, sent as a marked control frame
(`protocol.EscapedControlFraming`, see `wire_framing.md`), built and parsed by
`protocol.EncodeProgress` / `protocol.ParseProgress`
(`internal/protocol/progress.go`). The fields are blank-separated
`key=value` tokens: a parser ignores unknown keys and takes missing ones as
0, so later servers can add fields.

| field | meaning |
|-------|---------|
| `bytes` | bytes the session's file reads consumed so far (compressed bytes on disk for `.gz`/`.zst`) |
| `total` | summed size of the files the session reads |
| `files` | files whose read ended |
| `filestotal` | files the session reads |

All four describe the session's current generation: every read command of a
plain command stream (generation 0), or of the latest `SESSION
START`/`UPDATE`.

**Compatibility.** The frame is part of protocol 5 without a capability and
without a protocol version change. A protocol 5 client of before it ignores
it, as `handleHiddenMessage` drops unknown hidden messages. A client talking
to a dserver of before it gets no frame and shows no progress; with
`--progress`, its final line reads `reporting=0/N`.

## Server side

**What is counted.** Only snapshot reads of files are counted: cat and grep
mode, which includes dmap's reads, as those are cat and grep commands feeding
the aggregate. The following are not counted:
- tail mode (dtail), which has no end;
- `journal:` targets and stdin, which have no size;
- the scheduler's shared group reads (`readWithGroup`).

**Registration.** `readCommand.readFileIfPermissions` registers each file as
soon as the session's permission check of it passed (`trackProgress`,
`internal/handlers/read_progress.go`), with its size from `os.Stat` of the
resolved path. The total is therefore complete at once, also for globs with
more files than `MaxConcurrentCats` that wait for a cat slot. Denied and
missing files are not counted. The read is reported as a failure as before.

**Counting.** `fs.ReadOptions.Progress` (an `fs.ByteCounter`) reaches
`ReadFile`. In `makeFileReader`, `countedSource` hands the counter the open
file (`Opened`) and wraps the descriptor in a `countingReader`. The wrapper
sits below bufio and below the gzip or zstd decompressor, so a read costs one
atomic add per buffer fill, not per line, and compressed files count the bytes
on disk. Each reopen starts the file's count over.

**Growing files.** A log can be appended to while it is read, and a cat or
grep read goes on to the end it finds. Every report tick
(`fileProgress.refreshSize`) re-stats the files being read through their open
descriptors, so a file rotated away meanwhile is still the one measured. Only
open files are stat'ed: at most `MaxConcurrentCats` per session. A file read
past its last known size counts with the bytes read as its size.

**End of a file.** When a file's read ends, it counts as read in full
(`fileProgress.finish`), however it ended: EOF, `--max`, cancellation or a
read error. The final report then says 100%, and failures are reported by
`.syn command failed` as before. A newer generation starts the tracking over;
read commands of an older one are not tracked.

**Sending.** A goroutine per session (`readProgress.report`), started at the
first registration, posts the totals every second (`progressReportInterval`)
when they changed. `readCommand.readFiles` posts once more when all files of
its command ended. Posts go to a one-slot mailbox on the output coordinator
(`outputCoordinator.postProgress`, `internal/handlers/output_progress.go`)
instead of the server message queue:
- A post never blocks. It replaces a frame not sent yet, so only the latest
  numbers wait.
- `outputCoordinator.next` takes the mailbox **ahead of payload**, at a
  payload frame boundary (`tryReadProgress`). Payload goes before server
  messages, so a cat that keeps the session output buffer full would
  otherwise hold every progress frame back until its read ended.
- `tryReadQueued` takes it before `tryFlushOrClose`, and `queuedUnits`
  counts it. The final progress therefore always precedes `.syn close
  connection`.
- The frame carries its generation (`encodeGeneratedMessage`), so a frame
  of a replaced generation is dropped (`readServerMessage`).
- While a SESSION acknowledgement holds back output, or messages deferred
  for one wait (`sessionAckGate.pending`), the frame stays in the mailbox.
  It overtakes none of them.

Serverless mode uses the same handler and coordinator. Its payload bypasses
the frames, but its control frames do not.

## Client side

- `baseHandler.handleProgressMessage` (`internal/clients/handlers/progress.go`)
  keeps the latest progress of the session. `MaprHandler` passes hidden
  frames to the same switch. `Progress()` and the `ProgressReporter`
  interface, asserted rather than added to `Handler`, expose it.
- `baseClient.progressTotals` (`internal/clients/progress.go`) sums the
  handlers of all connections and counts the servers that reported
  (`reporting`).
- `progressMeter` measures the rate over the last 15 s
  (`progressRateWindow`). It starts the rate over when bytes or total
  shrink, i.e. a new workload after an interactive reload. ETA is
  `(total-bytes)/rate`.
- The stats loop measures the progress every second (`progressTick`,
  `stats.tickProgress`) and shows a changed one in one of two ways:
  - **Default:** as fields of the STATS line (`bar`, `progress%`,
    `progress`, `rate`, `eta`, `files`, `reporting`, `bytes`, `total`), checked every 3 s
    (`statsTicks`, `stats.tick`) against what the last STATS line showed,
    dropped by `--quiet`/`--plain`. The Ctrl-C statistics carry them too.
  - **`--progress`** (`Args.Progress`, all clients, meant for dcat, dgrep
    and dmap): as a `PROGRESS|bar=[######--------------]|progress%=30.0|...`
    line (the bar first, one `#` per 5%, so successive lines show it filling
    up in place; `progressReport.bar`) every second in a fixed field order
    on stderr (`stats.writeProgress`), whatever the quiet mode. After the sessions
    ended, `baseClient.Start` writes one final line
    (`stats.finishProgress`). Interactive sessions and the dmap query builder
    write no PROGRESS lines, as stderr would bypass the paused output and the
    line editor.
- While no server reported, `progress%`, `rate` and `eta` are `unknown`.

## Tests

- `internal/protocol/progress_test.go`: encoding and parsing.
- `internal/io/fs/progress_test.go`, `progress_zstd_test.go`: the counted
  bytes equal the size on disk for plain, gzip and zstd files; follow reads
  count nothing.
- `internal/handlers/read_progress_test.go`:
  - totals, files counted in full, generations, a file growing while it is
    read;
  - the mailbox ahead of queued payload;
  - the final progress before the close handshake for dcat's and dgrep's
    command stream in server and serverless mode.
- `internal/clients/handlers/progress_test.go`: client and MapReduce
  handler, invalid frames, unmarked look-alikes printed as payload.
- `internal/clients/progress_test.go`: rate window, ETA, formatting, STATS
  fields, PROGRESS lines and the final line.
- `integrationtests/progress_test.go`: dcat, dgrep and dmap with `--plain
  --progress` over a plain and a gzip file, against dserver and serverless.
  The final line must count every byte. Stdout must hold no PROGRESS line and
  equal the run without `--progress`.
