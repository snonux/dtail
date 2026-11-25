# Failed Scheduled Jobs

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

**Failed scheduled jobs: strict until the TimeRange ends (dserver):**
The scheduler skips a job for the rest of its date period once its outfile
exists, so a scheduled job writes its outfile and `.query` file (both via
`.tmp` and rename, no interim results) only as described here. Two kinds of
failure are told apart:
- **Transport failures** never write an outfile, not even after the job's
  TimeRange ended: no server connection at all; a connection that ended with
  a non-zero status (refused connection, failed SSH handshake, rejected
  session); the job's context canceled (e.g. the scheduling dserver's own
  shutdown); a session that ended without the server's close handshake
  (`.syn close connection`, sent only after all of the session's output),
  e.g. a remote dserver killed with SIGKILL, stopped with SIGTERM/SIGINT,
  crashed, or a lost connection; the client refusing to send a job's read
  commands to a server lacking `journal-v1` for `journal:` targets in its
  `Files`, which sets exit status 1;
  and a failed command that is no file read
  failure (`command: unable to decode command`, `command: rejected`,
  `command: unknown command`, `map: invalid query`, `map: incomplete result`,
  `read: unable to parse command`, `output: too large for the session output
  buffer`, or a reason the client does not know). `map: incomplete result`
  means the map command's final result did not reach the client in full (see
  "Server-Side MapReduce" above). The output reason means a read dropped line
  frames that did not fit the session's output buffer (see the output limits
  above); it is no file read failure, as the file was read. A scheduled job's
  server-side MapReduce sends aggregates, not line frames, so it cannot get it.
  dserver does not run the read commands that follow a failed map command of
  the same command stream (plain command stream or session generation,
  `internal/handlers/mapqueryfailure.go`): each reports `map: invalid query`
  too, so no line is streamed as plain payload in place of MapReduce results.
  This includes a map command rejected before it runs (e.g. options that do
  not parse), which itself reports `command: rejected`. A SESSION START or
  UPDATE builds its map command before it changes the session, so one that
  can not be built rejects the request and a running generation keeps
  running.
- **File read failures**, reported by dserver with the hidden message
  `.syn command failed <reason>`
  (fixed reasons from `internal/protocol/session.go`, no paths or error
  details; those stay in the server log), sent before the close handshake
  (`protocol.IsFileReadFailure`): `read: no file to read` (a glob matched no
  file after its retries, a file vanished or is a dangling symlink, or every
  path a glob matched is no regular file), `read: no permission to read file`
  (denied by the read permissions), `read: unable to create file reader`,
  `read: unable to read file` (open/read error, e.g. permission denied on the
  file system) and `read: more files than the server reads` (the glob matched
  more than `MaxGlobTargets`; only the first ones are read). This covers a
  file of the job that does not exist yet (e.g. `$today`'s log), a
  comma-separated `Files` list with a file that never appears, a glob matching
  no file, and a multi-server job whose file is missing on one host.

Directories and other non-regular files a glob matches are skipped silently
(server log at INFO, no client warning, no failure), unless the glob matched
nothing else (then `read: no file to read`). Read permissions are checked
before this, so a denied path reports `no permission` whatever it is.

Within the job's TimeRange (`clients.ScheduledMode`) any failure blocks the
outfile: the job writes nothing, keeps an outfile of an earlier run untouched,
and logs `Job <name> failed and wrote no outfile <path> (failure <n> in a
row), it runs again from about <time>`; the client logs why (`Not writing the
mapreduce result as the query did not complete` for transport failures, `Not
writing the mapreduce result as files could not be read ...` with
`<server>: <reason>` entries for file read failures). The job is backed off in
memory (`internal/jobs/backoff.go`, keyed by job and filled-in outfile): it
runs again 1, 2, 4, 8, 16, 32 and then every 60 minutes after the start of
its last failed run, but at least half that time after the run's end (a job
running longer than its backoff does not run back to back), on the first
scheduler run (every minute) from then on, with 5 seconds of slack for
scheduler drift. Runs skipped by the backoff are logged at DEBUG only.

Once the TimeRange of a failed run has ended (its end hour on the day of the
run; for `[0, 24]` that is the next midnight, when the `$today` period of the
outfile has passed; a job with no or an empty TimeRange never runs at all),
the scheduler runs **final runs** (`clients.ScheduledPartialMode`) with the
files and outfile (dates filled in) of the job's last failed run within its
TimeRange, before the scheduler run's other jobs: the first at the first
scheduler run from the range end on (ignoring the backoff), later ones after
the backoff, which goes on counting. Due final runs are grouped like runs
within the TimeRange (same files, servers and discovery, no conflicting
outfiles or reads, configured job order, waves of the same bound), and each
wave shares its reads, so jobs that failed together read their files once for
their final runs too. A final run logs `Starting final run of job <name> for
outfile <path> after its TimeRange ended at <time>, ...`. If its only failures
are file read failures, it writes what it could read (as every run did
before runs within the TimeRange became strict)
and the client logs `Writing partial mapreduce result after the job's
TimeRange ended|<server>: <reason>; ...`; if every file could be read by then,
it writes the complete result. A transport failure still writes nothing and
the final runs go on after the backoff. The scheduler gives up on an outfile
24 hours after its TimeRange ended (`Giving up job <name> after <n> failures
in a row: it wrote no outfile <path> within 24h0m0s ...`), and skips (and
forgets) a final run whose outfile exists by then. For an outfile without
dates, the next day's TimeRange takes over: its first run ignores the previous
range's backoff, a failure within it starts the failure count and the final
runs over, and runs within it are strict again and move the range end.
Failures are forgotten when the job writes the outfile and with a dserver
restart: after a restart within the TimeRange the job runs strictly again, but
a dserver restarted after the TimeRange ended does not know the failed run and
writes no outfile for that period.

Compatibility: every protocol 5 dserver reports failed commands, and a peer
of another protocol version is rejected (see "Wire Framing"), so a scheduler
always gets the failure reports and the close handshake. Continuous jobs and interactive `dmap` with an outfile
keep writing interim and final results whatever the exit status. A job whose
server discovery yields no server at all (e.g. a server list file of only
blank and comment lines; empty `Servers` mean this dserver, see `jobServersStr`
in `internal/jobs/runner.go`) fails when its client is created
(`clients.ErrNoServers`, `no server to connect to`, logged at WARN as `Unable
to create job <name>`): a scheduled job counts that as a failed run as above,
and a continuous job writes no outfile and runs again at its next retry,
every minute, without a backoff.
