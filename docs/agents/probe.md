# Probe Command (field discovery, path listing)

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

`probe <base64 JSON {id, files}>` asks dserver (and the serverless runtime)
which MapReduce tables, fields and `$variables` the lines of some files carry,
for the interactive query completion; it is answered once with the hidden
control frame `.syn probe <id> <base64 JSON>` (`protocol.ProbeAnswer`:
`defaultLogFormat`, `formats` of every registered log format that found
fields, `filesSampled`, `linesSampled`, `truncated`, `errors`). Wire types
and limits: `internal/protocol/probe.go`; command:
`internal/handlers/probecommand.go`, `probe_targets.go`, `probe_answer.go`;
full description: `docs/dev/probe.md`.
- Entries are read command path words and get exactly a read's checks:
  `protocol.DecodeReadPath`, `journal:` targets only with `journal-v1`, one
  `filepath.Glob` capped at `MaxGlobTargets`, the `readfiles` permission check
  of every match (`ServerHandler.PrepareReadTarget`); never stdin (`-`).
- At most 8 distinct files per request (`protocol.ProbeMaxFiles`; a file
  named twice, e.g. by a path and a glob, is sampled once), each a bounded
  sample (`probe.Sample`: last 256 KiB / 500 lines, compressed files' first
  500 lines, `journalctl -n 500`, the tail sample's first line put in front
  for csv headers but not counted in `linesSampled`), no cat/tail limiter
  slot. Every step is bounded by size; a 10 s timeout (`probeTimeout`) also
  stops the sampling (and `journalctl`), not the glob, permission checks or
  log format probes. The answer JSON is
  capped at 48 KiB: sample values go first, then fields (the default log
  format's last), with `truncated` set.
- Per table: a format's `tableVariables` maps every table to the
  `$variables` its lines filled, also for one table, an empty entry for a
  table none of whose sampled lines filled one, never known variables
  without values; only a custom probe without per-table data leaves it
  out, and only then does `logformat.ProbeResult.VariablesOf` fall back to
  the format-wide `variables`. The sample window is streamed and every line
  of it, also those beyond the 500 kept, is passed to the table scanners
  (`fs.SampleOptions.Observe`, `logformat.ProbeTableScanner`,
  `ProbeTabler`), so the answer lists every table of the window (without
  fields, with an empty `tableVariables` entry, where none of its lines was
  kept). A panicking `ProbeTabler` is recovered and reported as `probe: log
  format probe failed` for its format. Only tables a query can name are
  listed: `from stats` reads `STATS` (`mapr.TableName`) and lines are
  matched by the exact name, so the default format's probe and
  `ProbeLineTable` put a line whose table name holds lower-case letters
  (`mapr.SelectableTable`; `MAPREDUCE:stats`) under the table-less entry
  `""` (`ProbeAnyTable`): a query without a table (no `from`, `from .`)
  reads it, so Tab offers its fields only while no table is typed and
  `:fields` shows them as `fields:`; the probe's `note` names the first
  such table of the kept lines and how to query them.
- The `csv` format answers only for sources that look like csv files
  (`csvProbeSources`, `internal/mapr/logformat/csv_probe.go`: a first line of
  at least two non-empty columns of at most 64 bytes, only those listed, at
  least half of the sampled rows of 2 up to the header's columns, and no
  more than half of them, once 3 rows were sampled (`csvProbeMinEchoRows`),
  echoing the header, `csvProbeSource.echoesHeader`:
  a value equal, but for the digits of its numbers, `csvSameShape`, to the
  name of its column, for names with a letter), so other logs get no bogus
  csv field named after their first line, also not a free-text log of lines
  like `request 1 done, ok`. Heuristic: single-column csv files and
  headerless csv files whose rows mostly repeat a value of the first are
  never probed; a free-text log whose lines hold a comma still is when most
  lines differ from the first by more than numbers on both sides of it.
  All 21 multi-column csv fixtures of the integration tests stay probed
  (`TestCSVProbeKeepsFixtureCorpus`).
- Table-targeted probe (`{id, files, table}`, `internal/handlers/probe_table.go`):
  same checks, file cap, timeout, one-probe-per-session and no limiter slot
  as above, but it scans 4 MiB of each file (`probeTableScanBytes`;
  `ScanBytes` of a head sample, 5,000 `journalctl` entries), streamed, and
  keeps (500 lines / 256 KiB) only the lines of the table
  (`ProbeTableScanner.Matcher`: the table a format's `ProbeTabler` assigns,
  else, for lines no `ProbeTabler` claims, the name as a word, or as the
  first 64 bytes, `ProbeMaxNameBytes`, of a longer word, the name such a
  table is listed under); the answer
  (`table` echoed) holds that table only, with its fields and
  `tableVariables`, no format-wide `variables`. An invalid table
  (`protocol.ValidProbeTable`: no blanks, control/format characters, `|`,
  `,`, quotes or backticks) or a table with `list` is `probe: invalid
  request`.
- Failures are `errors` entries with fixed reasons, never `.syn command
  failed`, so a probe changes no exit status. One probe at a time per session
  (another is answered `probe: another probe is running`), and at most one
  answer waiting to be queued (`probeInFlight`, `startProbe`): the next
  probe waits for it on the command input at most 500 ms
  (`probeInFlightWait`) and is answered busy then, so neither a client that
  stops reading nor a payload flood (payload is read before server
  messages, so answers can stay unread while the client reads) holds the
  command input or makes dserver keep a goroutine and answer per probe.
  Busy/invalid answers never wait: dropped when the server messages are
  full (`trySendProbeAnswer`). A probe carries no
  session generation, so it runs next to a `SESSION` workload without
  replacing it, and it never ends the session; it only starts an idle
  shutdown that other work asked for while it ran
  (`commandShutdownIfWanted`).
- Logging: a probe's permission checks (`ServerHandler.probeReadTarget`,
  `sessionuser.User.ResolveReadTargetQuietly`, same result as
  `PrepareReadTarget`) and its per-file `Probe: unable to read file` lines
  log at DEBUG only; each probe or listing logs one INFO line, `Probe
  answered` (entries, errors), and a field probe one WARN `Probe: unable to
  read entry <entry> <reason> matches <n>` per request entry and reason
  that failed (`probeRun.entryFailed`; at most 8 entries, entry cut to
  256 bytes), so denied or missing targets stay visible at the default log
  level without one line per glob match. A read command's check keeps its
  INFO lines.
  A listing's directory that can not be opened or read (`Probe: unable to
  list directory`) logs at DEBUG too, and a rule that does not compile is
  logged at WARN by a user's first listing only (`User.PathRules`), as any
  user starts listings with Tab.
  A user's permission regexes are compiled once per `User`
  (`compiledPermissions`), not per check or listing. Measured listing of a 500-file
  directory: 1000 INFO lines and 13 ms / 10.8 MB (1 rule), 83-171 ms /
  60 MB (5 rules) before; 0 per-file lines and about 6 ms / 1.7 MB after
  (`BenchmarkProbeList500`).
- Path listing (`internal/handlers/probe_list.go`): a request `{id, list:
  "<prefix>"}` (no `files`) is answered `{id, dir, entries: [{name, dir}],
  truncated}` for the directory part of the prefix (names starting with the
  rest, sorted, at most 500, answer within 48 KiB). Files only if
  the read permission check allows them. Directories unless no allow rule can
  still match a path beneath their resolved path or one deny rule already
  matches `<dir>/` itself: within it, or only at its end through an
  assertion about what follows (`$`, `\b`) and then also the sample paths
  `<dir>/x`, `<dir>/x/x`, `<dir>/.x`, `<dir>/y/y/y/y.y`, `<dir>/-` and
  `<dir>/-/-` (`denySamples`), so `!^/var/log/secret/.*$` hides `secret` while
  `!^/var/log/[^/]*$`, `!^/var/log/(old/.*)?$` and a rule of one and two
  levels only (`!^/home/[^/]*/?[^/]*$`) keep their directory, whose
  subdirectories hold readable files, offered (a rule that matches `<dir>/`
  itself and everything beneath, `!^<dir>/` or `!^<dir>/.*$`, matches every
  sample and always hides; `!^<dir>/.+$` does not match `<dir>/` and hides
  nothing; `sessionuser.PathRules.MayReadBeneath`, rules compiled once per
  `User`, an NFA run of each rule over the directory path; at its end the
  assertions are tried per kind of next rune, word, non-word, end of text,
  `endOfPrefix`, so contradictory ones such as `\b\B` keep no allow rule
  alive; an unanchored allow rule that may start a match after the prefix,
  e.g. at the end of a longer path, `$`, `\b$`, `(?m)$`, counts as alive
  everywhere, `matchesLater`). This is an
  approximation that reveals some directory names: a directory denied only
  by what a deny rule requires after its `/` (`!^/var/log/private/.*\.log$`,
  `!^/data/x/[^/]`) or by several deny rules together is offered (only names
  an allow rule covers; a glob read's no-permission/no-file answers reveal
  as much). The listed directory must pass the same rule, and a
  missing, unreadable or forbidden one is the same empty listing. Checks use
  resolved absolute paths (symlinks, relative prefixes; a `..` is cleaned
  lexically first, as reads do, so it is no existence oracle of the component before
  it); hidden names only for a `.` prefix; bounded scan (100,000 names,
  5,000 checks, at most 10,000 names held, the probe timeout; a bound
  sets `truncated` also without entries, so the client fetches a narrower
  prefix, an accepted oracle that many names not offered share a prefix,
  as is the listing's timing; a timeout is `probe: timed out`). `journal:` prefixes list `journalctl --field
  _SYSTEMD_UNIT` units the user may read as `journal:<unit>`, only with
  `journal-v1` and only when an allow rule can match a target with the
  prefix (else no `journalctl` run). The unit list is shared by all
  sessions of a dserver (`journal.UnitCache`, `internal/io/journal/unitcache.go`,
  `handlers.NewJournalUnitCache`, `Dependencies.JournalUnits`): a
  successful list is reused for 60 s (`journal.DefaultUnitCacheTTL`;
  failures are not cached), concurrent listings share one `journalctl` run,
  which belongs to none of them, so the probe timeout does not bound it:
  it goes on when every listing waiting for it gave up (until dserver
  shuts down, `UnitCache.Close` in `Server.stopSharedWork`) and its result
  is cached (a journal slower to list than a probe's 10 s was never cached
  before), and it stops at the latest after 30 s (`unitFlightTimeout`),
  which alone bounds a hung `journalctl`; a panic of the lister is recovered into
  the run's error (logged with its stack at WARN). The permission
  filtering stays per user and request. Serverless mode does not cache. `Server.PathCompletionDisable` answers `probe: path listing
  disabled`.
- Client (`docs/dev/probe.md`, "Client"): only interactive sessions probe,
  in every mode. `baseHandler.handleHiddenMessage` queues answers for
  `handlers.Prober.Probe` (`internal/clients/handlers/probe.go`; ctx-aware,
  stale ids skipped, never blocks the frame reader; one call per handler at
  a time: a concurrent call waits for the slot while its ctx allows, so an
  on-demand path listing from Tab shares it with the background field probe
  instead of failing or preempting it, decision N1: preempting would free
  only the client's slot while dserver went on sampling and answered
  `probe: another probe is running`). The
  background `interactiveProber` (`internal/clients/interactive_probe.go`,
  started by `startInteractiveControl`, stops with the session) asks at most
  3 connections (re-read each round, so reconnects are fine; `probeTargets`
  takes those whose session is up first, those still connecting only fill
  the slots left, and never one whose session ended; a connection whose
  `Start` ended without a session, e.g. a server down, has its handler
  shut down by `ServerConnection.Start`, so it neither holds a round or
  `:probe` for the 30 s round timeout nor takes the path listings): at start,
  after every applied `:reload` (`request()`), every 60 s while the output is
  not paused; debug logging only; after each requested round (not the
  periodic ones), once it counted as finished, the path listings of the
  files' directories not cached fresh (after `:probe`: all of them) are
  requested in the background. Answers go into the mutex-guarded
  `fieldCatalog` (`interactive_catalog.go`: latest answer per server with
  its probed files, merged on `snapshot(files)` over the answers about the
  session's current files, so answers about files of before a `:reload`
  no longer count), which Tab completion and `:fields` read. Constraint, settled by
  `TestProbeAnswerWhileOutputPaused`: with a remote dserver no answer
  arrives while the output is paused (the paused writer blocks the handler
  reading the frames, the answer is queued behind payload); in serverless
  mode a field probe sent before the pause does (payload bypasses the
  handler), but a request sent while paused is not handled until the output
  runs, as the in-process server logs to the paused output. The query
  completion waits for a server only for a table probe (below); the path
  completion waits at most 1 s. During both waits the paused writers go on
  into a bounded hold instead of the terminal (`interactivePause.holdOutput`,
  see "Tab waits hold the output"), so the answer arrives and nothing is
  printed over the line being typed.
- Table probes (`interactiveProber.requestTable`,
  `interactive_probe_table.go`): when Tab completes in a `--query` value
  with a typed `from` table (`completeWithTableProbe`; only the Tab that
  sends it waits, like for a path listing, 1 s with the paused output held; a
  Tab while it is on its way completes from the cache with a hint) and on
  `:fields TABLE` (`awaitTableProbe`, 2 s), a table-targeted probe of the
  current files is sent to the round's targets, unless one is on its way
  (checked first, under the same lock), the catalog holds a table answer of
  that table and files younger than 60 s (`hasTableProbe`, also one that
  found nothing) or one no server answered (failed, timed out, busy) within
  5 s (`markTableProbeFailed`, `tableProbeRetryAfter`). Answers are kept per
  server and table (`storeTable`, at most 64) and merged into the snapshot
  of the same files (`catalogSnapshot.TableProbes`);
  `catalogFormat.variablesOf(table)` gives only the `$variables` a table's
  lines filled (format-wide only for answers without per-table data,
  `answerTableVariables`), which is what the query completion offers
  once a `from` table is typed.
- End to end (`integrationtests/interactive_guided_test.go`, dserver and
  serverless): `TestInteractiveGuidedQuerySession` types the target
  session of the guided completion with Tab on a PTY (`--noColor`, no SGR
  in the output) in a `dtail` following a log of two tables, checks that
  the listings hold only the chosen table's fields and `$variables`, runs
  the query (result table, no followed line meanwhile), `:tail` back and
  `:query` again on one session; `TestInteractiveQueryTableScope` checks
  the tables Tab lists after `from`, where values per table, and a table
  that only appears before the probe's window, found by the table probe
  of the Tab after `from RARE` and listed by `:fields RARE`.


## Field probe sampler

- **Field Probe Sampler**: `probe.Sample` (`/internal/mapr/probe/sample.go`)
  returns a bounded, one-shot sample of one validated read target for the
  MapReduce field probe; it does no permission check (the caller resolves the
  target with `ResolveReadTarget` first). Uncompressed files: the lines
  starting in the last 256 KiB (partial first line dropped), at most 500, via
  `fs.SampleTail` (`/internal/io/fs/sample.go`, `ReadAt`, file offset
  untouched), plus the file's first line in `fs.Sample.FirstLine` when the
  window starts past offset 0, so a csv probe can still see its header.
  gzip/zstd files (by path suffix, as read commands): the first 500 lines
  (`fs.SampleHead`; `.zst` fails in `DTAIL_NO_ZSTD` builds); a head
  sample stops after scanning 4 x 256 KiB of decompressed input
  (`fs.SampleHeadScanFactor`, skipped lines and terminators included, plus
  the rest of the line or `MaxLineLength` piece crossing it), so a file of
  oversized lines only is never decompressed to its end. `journal:unit`
  targets (one literal unit name with its type, `fs.validateJournalSpec`):
  `journalctl -u unit -n 500` through the journal reader (error
  without `journalctl`/off Linux). The lines of every sample are at most 500 and
  256 KiB of line data (`fs.SampleOptions`; `FirstLine` comes on top), cut
  at `MaxLineLength` with `fs.SplitLine` and returned without their LF/CRLF
  terminator; a line larger than 256 KiB on its own is skipped, so it neither
  evicts nor ends the sample. The tail window is streamed (64 KiB buffer),
  never held whole. Options of a table probe: `ScanBytes` (the tail window
  and head scan bound instead of the defaults), `ScanLines` (`journalctl
  -n`), `Keep` (lines kept; the others count towards the scan bounds only;
  `FirstLine` is then also returned when the window starts at 0 but its
  first line was not kept) and `Observe` (sees every scanned line).
