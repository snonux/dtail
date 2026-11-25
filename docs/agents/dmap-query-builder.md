# dmap Query Builder

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

`dmap` started without a query opens a query builder instead of failing with
`no mapreduce query specified`, when stdin and stdout are a terminal and the
control TTY opens (`cli.InteractiveDefault.ResolveQueryBuilder`, bound with
`BindQueryBuilderDefault`, the same terminal rule as dtail's interactive
default). `--no-interactive` (new for `dmap`) never opens it and wins over
`--interactive-query`; `--interactive-query=false` does not open it either;
an explicit `--interactive-query` opens it also without a terminal. With a
query (`--query` or positional) nothing changes, and without a terminal (pipe,
script, cron, the jobs) a missing query stays the error, exit status 1.
`cmd/dmap/main.go` decides in the runner's `BeforeRuntime` hook, i.e. after
`config.SetupRuntime` took a positional query.

**Design: two phases, no dserver change** (`clients.QueryBuilderClient`,
`internal/clients/querybuilderclient.go`; the builder's part of the control
loop is `internal/clients/interactive_builder.go`):
1. *Builder.* Interactive sessions that hold no workload, on at most
   `probeMaxConnections` (3) servers (see "Held sessions" below):
   `SESSION START` with `builderHoldSpec`, tail mode without files, which
   yields no command. That is what an interactive `dtail` without `--files`
   sends, so every protocol 5 dserver and the serverless runtime keep such a
   session (`sessionCommandState.keepAlive`,
   `TestSessionStartWithoutFilesHoldsTheSession`); map mode can not hold one,
   as dserver rejects a map spec without query (`missing session query`).
   The `--files` are not in the held spec (a tail spec with files would
   follow them): they stay in the client's args and the prober reads them
   from there (`baseClient.sessionFiles`). So the same `interactiveProber` /
   `fieldCatalog` probes them as in interactive `dtail` (field probe at
   start, table probes on Tab after a typed `from` table and for `:fields
   TABLE`, path listings), and the same `interactiveEditor` edits the line.
   A server that is down logs its connection error and the builder stays
   usable.
2. *Run.* Enter on a query that parses hands query and files to the client
   (`clientProfile.queryBuilt`, `runBuildCommand`) and ends the builder's
   sessions like `:quit`; `QueryBuilderClient.Start` then starts
   `NewMaprClient` with them on new connections: the plain command stream,
   the server's close handshake, the result table or outfile and the exit
   status of `dmap --query`, unchanged. With an explicit
   `--interactive-query` that client is interactive `dmap` (its own
   `SESSION START` with the query; the session stays, `:query` replaces it).

Why the query does not run on the held session: a session started with
`SESSION START` never ends by itself, so the client could not tell when the
result is complete without a new signal from dserver, which would mean
redeploying every host. The run costs one more connection per held server
instead (cheap with the auth-key fast reconnect), and `dmap --query` stays byte
for byte what it was. `TestDMapQueryBuilder` expects exactly two sessions on the
server, the map command only on the second.

**In the builder** (`config.Args.QueryBuilder`, set only by
`NewQueryBuilderClient`; `isQueryBuilder`): the line typed is the query, no
`:query ` needed (`:query QUERY` works too); a line starting with `:` is a
control command (`parseBuilderCommand`): `:fields [table]`, `:probe`, `:show`
(`workload=builder`, the files), `:help` (`builderHelpText`), `:quit` (leaves,
nothing runs, exit status 0 unless a connection failed), `:reload --files
PATH` (`--files` is the only flag of its flag set; it only changes the
client's files and asks the prober for a round, no `SESSION UPDATE`); `:tail`
is rejected. Tab on a line that is no control command completes it as a
query with the completer of `:query` (`completeBuilderQuery` ->
`completeQueryText`), so Tab on the empty line starts with `from`. A query
that does not parse is answered `query builder: invalid query: ...` and the
builder stays open; so is a query while the builder has no files (`query not
run, the session reads no files: ...`). The banner is `builderBanner` (one
line; the no-files hint follows it as in every session), every line has the
prompt `query> ` (`builderPrompt`, `interactiveEditor.builder`), typing shows
no pause hint, and an empty line pauses nothing (it is answered with what to
type): there is no output to pause, only the client's own log lines, which
the typing pause still holds back while a line is edited, also during a
Tab's wait for a table probe or path listing (kept in the hold then, see
"Tab waits hold the output"), and which every
entered line releases (`handleBuilderLine`). The builder runs with `Quiet`,
so no periodic connection STATS are printed. A command word typed without
its colon (`quit`, `exit`, `q`, `help`, `show`, `fields`, `probe`, `reload`,
`query`) is answered with the command meant (`"quit" is no query: type
:quit`, `bareCommandHint`) instead of the parser's error. `:reload --files`
with a `journal:` target is refused, files unchanged, while a server whose
session is up lacks `journal-v1` (`checkReloadJournalCapability`, as for a
reload of a running workload; a server not connected is checked by the run).

**Held sessions** (`internal/clients/interactive_builder_sessions.go`): the
builder holds sessions only for its prober, so at most `probeMaxConnections`
(3) at a time, whatever the number of servers (`holdBuilderSessions`,
`serverRotation`): a session that ends (server down, idle close, restart) is
replaced after the reconnect backoff (2 s doubling to 60 s with jitter,
started over after a session that lasted over 60 s: `retryBackoff` and
`baseClient.awaitReconnect` in `baseclient.go`, the one policy that a
reconnecting `dtail`, `startConnection`, and the builder, `holdSessions`,
both wait by; they differ only in what follows: a connection that can not be
created again ends `startConnection`, while the builder counts a failed
session and goes on, and logs `Reconnecting` with the server it goes on
with, known after the wait) by one to the
next server not held, in the client's order and around, so a server that is
down is tried once per round of the rotation; with one server that is a
reconnect. The prober asks the held connections only
(`baseClient.probeConnections`; a new held session requests a round), so the
builder's catalog is those servers' view, as in dtail it is the probed
servers'. The run connects to every server. Every 60 s
(`builderKeepAliveInterval`, `keepSessionsAlive`) an SSH `keepalive@openssh.com`
request (`connectors.KeepAliveRequest`) goes over each held connection
(`connectors.ServerConnection.KeepAlive`, no reply awaited; each sent by a
goroutine of its own, at most one per connection, so a connection whose
write blocks does not delay the others,
`TestKeepSessionsAliveDoesNotWaitForABlockedConnection`; a compile-time
assertion keeps `ServerConnection` a `keepAliver`): dserver counts every packet read as activity and discards
unknown requests (`gossh.DiscardRequests`), so `IdleSessionTimeoutS` does not
close a held session while the query is built, also one without files or
with a line left unentered, where no probe is sent; no dserver change
(`TestIdleSessionKeptByKeepAlive`, a real dserver with a 4 s timeout, proves
dserver's side: the request, sent every second, keeps a connection; that the
builder sends it is covered by `TestKeepSessionsAlive` and
`TestKeepAliveSendsARequestTheServerReads`). The 60 s interval does not
cover every configured timeout: dserver closes a connection at the deadline
its last refresh tick that saw activity set, `IdleSessionTimeoutS` + 2
intervals after that tick (ticks every interval = timeout/4, clamped to
1-30 s), so a keepalive must be seen by a tick before that deadline. With
one every 60 s that always holds for a timeout above 48 s (60 s < timeout +
interval), never for one of 40 s or less (60 s >= timeout + 2 intervals);
in between it depends on where the ticks fall, so the session may be
closed. A closed session is replaced.

**Log lines** go through the line editor while the builder edits
(`clientlog.Redirect` -> `loggers.OutputRedirector`, implemented by the
`stdout` logger, delegated by `fout` and `DLog`; `openControlLines`, undone
before the editor closes; the redirect target gets whole lines only,
`loggers.wholeLines`, as the logger's 64 KiB buffer flushes itself in the
middle of a line when it is full and the editor draws the prompt below
every write; text without a line break is held until its line or the
redirect ends): one arriving at the prompt (a connection error, a
server warning) is printed above it and prompt and line are drawn again. Only
when stdout is the control TTY (`editorDeps.logOnTTY`, `sameTerminal` in
`interactive_tty.go`: the same character device file, or both the
controlling terminal of the process, `controllingTerminal`, TIOCGPGRP, as
the default control TTY `/dev/tty` is an alias with a device of its own);
output redirected to a file or pipe, and stdout on another terminal than
the control TTY (`--control-tty`), stays where it goes
(`TestSameTerminal`, `TestSameTerminalControllingTTY`,
`TestBuilderLogStaysOffTheEditor`). A fleet that is all down still logs each failed dial,
at most three per backoff step.

**Paste and pending input:** in the builder a bracketed paste of several
lines is one line (`editInput.joinPaste`: its line breaks become blanks, as
Alt-E does) that stays on the edit line until Enter is typed. Entered line by
line, as the editor does elsewhere, the first pasted line that parses alone
ran as the query and ended dmap, and the lines behind it were left in the
terminal's input queue for the shell. A paste without markers (a terminal
without bracketed paste) is told from typing in the builder only
(`internal/clients/interactive_tty_burst.go`), by what follows a line break
in the same read or within 30 ms of it (`editInput.settle`,
`enterQuietTime`, reads of the control TTY with a deadline): pasted text,
i.e. at least 16 bytes (`burstMinBytes`) or text holding another line break
(`pastedBehind`), starts a burst, which `editInput` passes on to x/term as a
bracketed paste of one line (start marker put in front, line breaks and
control characters blanks, a paste marker in its text is text), ended at
the first gap of 100 ms (`fillBurst`, `burstQuietTime`; the start of a
character cut by that gap is held for the next read). So the lines of such
a paste are not entered: the text stays on the line (x/term keeps the first
4095 characters; a longer line is rejected at Enter) until Enter is typed.
Anything less behind a line break is typing: the line break is the Enter
key and the one or two keys a person types within 30 ms (also Esc,
Backspace, an arrow, a held key or Enter repeating) are those keys, in
order; a real bracketed paste arriving in that window is left to its
markers. The price: an Enter is taken up to 30 ms late, and keys typed
ahead that reach dmap together with their Enter (typed while the control
loop was busy with a command or a Tab wait and read in one piece, or typed
within 30 ms) are taken for a paste when they are 16 bytes or more or hold
a second Enter; they then stay on the line.

When the builder ends, the control loop, after its last read and before
the TTY is closed, discards the control TTY's input for as long as it keeps
arriving: until it was quiet for 100 ms, at most 30 s (`drainTTYInput`,
`discardQuietTime`, `discardMaxTime`: tcflush TCIFLUSH, `discardTTYInput`,
`ttyflush_*.go`: Linux, AIX, Solaris and the BSDs/macOS, a no-op elsewhere,
and a read with a deadline, repeated; one tcflush only discards the 4096
bytes the kernel has queued, the rest of a paste arrives after it), so what
was typed or pasted after the entered line reaches neither the run nor the
shell. On `:quit` or a query to run, a cancellation of the session (Ctrl-C
twice, SIGTERM) ends the discarding at once. A session cancelled while the
builder is open is drained as well: the cancellation lets the control
loop's read time out (`controlReading.interrupt`, repeated every 10 ms
until the loop closes its lines; the editor's own timed reads, the window
behind an Enter and a burst of pasted text, which set and clear the read
deadline themselves, end at once through `editInput.stop`,
`interactiveEditor.stopReadsWhen`, as the deadline alone lost against a
paste that kept arriving, `TestEditInputBurstEndsWhenStopped`), the control
loop returns and drains in
its close, there with only the quiet time and the cap to end it (and the
forced exit of a further Ctrl-C), so a paste in flight at a Ctrl-C is
discarded and a quiet terminal costs 100 ms. Only when the control loop
does not return within 1 s (`controlReadInterruptWait`: it waits for a
command, the external editor or a blocked terminal) the cancellation
flushes once and closes the TTY, as before. A control TTY that is no
terminal (a pipe or FIFO feeding control lines) is not drained: its unread
lines stay (`TestDrainLeavesWhatIsNoTerminal`).

Not guaranteed: a paste whose first line is a valid query followed by 30 ms
of silence, or by less than 16 bytes without a line break in that time,
enters that line, and the query runs (the rest is then discarded by the
drain as long as it keeps arriving); input arriving after the drain ended
(a gap of 100 ms or more in a paste, keys typed later, anything after
30 s, or after a Ctrl-C ended the drain of a `:quit` or run) is the run's
or the shell's, as it would be typed at their prompt; and where reads of
the control TTY take no deadline (`os.File.SetReadDeadline` fails: Go's
runtime poller does not take every terminal device, e.g. kqueue refuses
`/dev/tty` on macOS; verified on Linux only) only input in the same read
tells a paste and the discard is one tcflush, as before.
`TestQueryBuilderPastedLinesAreOneLineOnPTY`, `TestEditInputBurstPaste`,
`TestQueryBuilderUnbracketedPasteOnPTY` (3 to 64 KB),
`TestQueryBuilderDiscardsInputInFlightOnPTY` (200 KB in 4 KB pieces),
`TestQueryBuilderDiscardsPasteInFlightAtCancelOnPTY`,
`TestQueryBuilderCancelDoesNotWaitOnAQuietTerminalOnPTY`,
`TestQueryBuilderFastTypingAfterEnterOnPTY`,
`TestQueryBuilderTypedEnterAfterQuietOnPTY`.

**History:** with `--interactive-query` the run's line editor starts with
the lines the builder took (`baseClient.lineHistory`, `keepHistory`,
`seedHistory`, `runHistory`), a query as `:query QUERY`, so Up recalls the
query built. Lines the builder rejected (those `parseBuilderCommand` fails
for: a query that does not parse, `quit` without its colon, an unknown
command) are left out, as `:query quit` would be recalled for them.

**Closing the control lines** (`openControlLines`): the close (statistics
and logger given back, the terminal's input discarded, the terminal mode
restored) runs once (`sync.Once`), by the control loop (before it cancels
the session on `:quit` or a query to run, and deferred) or at the
cancellation of the session, whichever comes first; the other waits for it,
so none of it happens after the control loop returned, when the run has the
logger and the terminal. In the builder the cancellation first makes the
control loop return, so that it is the one to close (see "Paste and pending
input").

**Files that look like a query:** a positional argument is the query only
when it holds `select ` (`config.setupAdditionalArgs`), so a query with that
word mistyped is taken for a file, no query is given and the builder opens.
When the first of several words of the files value is a clause keyword
(`mapr.Keywords`), the builder says so below its banner
(`queryAsFilesHint`, `builderQueryAsFilesHint`).

**Accepted limits:** the run needs the connection slots the builder just
released; the builder closes its connections before the run connects, but a
dserver at `MaxConnections` that has not yet noticed the close can refuse
the run, which then fails like any refused connection (status 1). Host key
answers are not carried over: a host refused in the builder is asked about
again by the run, as are the unknown hosts the builder never connected to.
`:quit` exits 0, or 1 when a held connection failed last (e.g. a server
down).

Tests: `TestResolveQueryBuilder` (start-up decision),
`internal/clients/interactive_builder_test.go`,
`interactive_builder_sessions_test.go` (held sessions, rotation, keepalive
loop) and `querybuilderclient*_test.go` (lines, completion, help, the
hand-over, the editor on a PTY: paste, pending input, log lines, history), and `integrationtests/dmap_builder_test.go` on a PTY
against dserver and serverless: `TestDMapQueryBuilder` (banner, parse error
of a query without `from STATS `, Tab inserts `from STATS `, the echo
`query: from STATS select ...` of the query run, which only the Tab's
insertion gives, result table, exit status 0, two server sessions; with
`DTAIL_TEST_BUILDER_NO_TAB=1` the Tab is left out and the test must fail), `TestDMapQueryBuilderQuit`, `TestDMapQueryBuilderStaysInteractive`
(`--interactive-query`), `TestDMapWithQueryOnTerminalRunsAtOnce` (`--query`
and a positional query open no builder) and `TestDMapWithoutQueryNoTerminal`
(a pipe and `--no-interactive` keep the error).
