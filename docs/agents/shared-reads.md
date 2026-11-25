# Shared Reads (follow reads, scheduled job groups)

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

**Shared follow reads (dserver):**
Sessions that tail the same uncompressed file share one reader of it in
dserver (`internal/io/fs/readhub`); `Server.SharedReadsDisable: true` turns
this off. Each session still passes its own permission check, holds its own
tail slot, and filters, numbers and processes the lines itself. A session
starts at the end of the file at the path as of its join, like a private
follow read: it skips published lines that were in the file before, also when
it joins after a rotation or copytruncate the shared reader has not caught up
with yet. The shared reader never waits for a session: one that falls more
than 64 chunks (about 64 KiB each) behind is evicted, logged at INFO with the
remaining subscriber count. It handles what it had queued (including a
rotation, truncation or reader panic that no longer fit) and goes on with a
private reader of its own target just past its last line, keeping its line
numbering and local context; a shared reader failing without a panic hands
every session over the same way. Every session holds its own descriptor of
the file the shared reader has open: a separate open (not a dup, so offsets
are not shared) through the session's own validated target, made when the
reader opens a file or the session joins and kept only if it is that same
file. The private reader starts in that descriptor, so a rotation loses no
line, also when the path was rotated before the eviction while the shared
reader was still behind in the old file (a burst larger than the queue
followed by a size-triggered logrotate). This is portable (no `/proc`) and
costs one descriptor per session, as a private reader does. The one
remaining gap: if the path is rotated in the moment between the shared
reader opening a file and a session opening its descriptor, that session has
none, and if it is later evicted with lines of the old file unread, it reads
the new file from its beginning and logs a warning. Follow reads of compressed
files and max-count (`--max`) follow reads read privately, as do journal
targets, stdin and serverless mode; one-shot reads (`dcat`, `dgrep`, `dmap`)
read privately unless they belong to a scheduled job group (below), whose
shared read also covers compressed files.

At a rotation, every follow read, shared or private
(`internal/io/fs/readfile_processor_optimized.go`), reads the old file to its
end, then polls it again 100 ms later, and after every poll that found more
lines, at most 10 times, for lines a writer that has not reopened the path yet
still appends, before it moves on to the new file. Those lines keep their
order and numbering. A last line of the old file that is still unfinished when
the draining ends is dropped, as a follow read always dropped it.

Output with sharing on equals output with sharing off, checked with
SIGSTOPped, context, late-joining (also right after a rotation or
copytruncate), `--query` `dtail` clients and a rotation right after a burst
that evicts lagging sessions (with and without a SIGSTOPped client). Apart
from the rotation gap above, two differences remain: a session that joins in
the middle of a line being written gets that whole line, where a private read
gets the rest of it from the join; and a trailing line the writer has not
finished yet is not passed on when a session leaves.

Long line warnings match too: a session sends one `Long log line, splitting
into multiple lines` warning per line cut at `MaxLineLength`, as a private
read does, also when it is evicted between a line's warning and its first
piece (it holds the warning until that piece arrives and leaves it to its
private reader otherwise, `sessionRead.longLineWarning`) or in the middle of
the line (its private reader does not warn again, `StartOffsetInSplitLine`).

A failed read reaches the client the same way, too. A private read reports
every failed iteration of its retry loop to the client with the hidden
message `.syn command failed read: unable to read file` (once per command,
see `protocol.HiddenCommandFailedPrefix`), and a shared read reports the same
for each of the four ways it fails: the session's processor or filter failing
(`readCommand.readShared`, which then restarts privately), one read of the
shared reader failing (it reads the file again, as a private reader does, and
every session of it is told), the shared reader failing for good without a
panic (each session goes on privately) and a failed read of a session's own
reader after an eviction. The hub reports the last three through
`readhub.Session.ReportFailure`, with the error that failed, which the read
command reports as its own read loop does (`readCommand.reportReadError`):
output a session's own reader after an eviction dropped as too large for the
session output buffer gets the `Dropped output too large ...` warning and the
reason `output: too large for the session output buffer`, not `read: unable to
read file`. A max-count stop is no failure in either
path, and a reader worker panic ends the session as a panic in both, without
a report.

An evicted session rejoins the shared reader once it caught up
(`internal/io/fs/readhub/rejoin.go`): whenever its private reader reaches the
end of the file at the start of a line, at offset P of file F, it asks
(`fs.ReadOptions.HandOverAtEOF`) to hand over. Under the entry's `publishMu`
the session is added back if the shared reader has F open, is not in the
middle of a read call (it flags each read before it starts, atomically, and
clears the flag under `publishMu` once it recorded how far it read), and has
neither read past P (e.g. an unfinished line: should F then be truncated to a
size between P and it, the reader would restart and publish the rewritten
lines up to P again) nor published a line ending past P; it then skips
published lines ending at or before P, so the hand-over loses and repeats no
line, and keeps its filter (numbering, context, max-count state), processor
and a held descriptor of F. If no shared reader exists (its last session left
or was evicted, or its reader failed, so the hub closed or dropped the entry),
the session starts a new one at P, reading from a descriptor of F opened
through its own target, and stays private when no such descriptor can be
opened, e.g. because the path is no longer F. Otherwise (reader in a read,
read or published past P, on another file, between reads, or no descriptor of
F to hold) it stays private and tries again at a later end of the file. A
declined attempt, or an eviction within 30 s of a rejoin, pauses the attempts
for 1 s doubling up to 30 s. Each rejoin is logged at INFO ("Evicted
subscriber rejoined the shared follow read", with the subscriber count). A
large burst (about 55-60 MiB written at once in the checks) can evict sessions
that are merely slower than the reader; they rejoin afterwards. In a check
with 10 sessions after a 300,000-line burst, dserver read about 40 MiB for the
next 200,000 lines instead of about 380 MiB without rejoin (CPU time
differences were within noise), and every output equalled sharing off.

Historical measured costs (N=4, 100 MiB; report retained in Git via
`git show 5eece8d:docs/shared-reads-benchmark-2026-09.md`):
scheduled groups read the file once and finish about 3.7 times sooner, at a
slightly higher dserver CPU (about 6-8%); a 100 MiB follow burst evicts every
session, so it costs what sharing off does; a paced follow (10 MiB/s) used
about 14% more dserver CPU with sharing on than off.

**Shared one-shot reads of scheduled job groups (dserver):**
The scheduler (`internal/jobs/schedulergroup.go`) starts due jobs that read
the same files from the same servers, and do not write each other's outfiles
or read files another writes, together as a group, when all their servers are
this dserver (`internal/jobs/localserver.go`), in waves of at most
`min(MaxConnections/4, MaxConcurrentCats)` divided by the number of servers,
and of at least one job, so that a wave is no larger than the group read the
hub shares among at most `MaxConcurrentCats` members; other jobs, and all
jobs when `Server.SharedReadsDisable` is set, run one at a time. Each set of
servers (a job's `Servers` and `Discovery`) is discovered and resolved once
per scheduler run for static server lists, not once per group that run forms,
and not at all while only one run is left pending, as there is nothing left
to group it with. For a static hostname the DNS answer is kept for that one
run; the next scheduler run refreshes it. FILE discovery (explicit or inferred
from a file path) and its hostname lookups run again for every wave because
each job's client reads
the file at connection time, and the file or DNS answer may change between
waves. A
scheduler run looks the servers up as soon as a due run is evaluated with
other runs still pending, even when those all turn out not to be due; a run
that is not due leaves the group formation before the lookup, so a scheduler
run whose only due run comes after the others needs no lookup at all.
Each job sends the option `share=<group>:<members>`, where members counts
the sessions of the group (its jobs times the servers each connects to, see
`scheduler.runGroup`); dserver honours it only
for the scheduler's user `DTAIL-SCHEDULE` and ignores it elsewhere. Each
member passes its own permission check and joins
with its own validated target. The hub (`internal/io/fs/readhub/oneshot.go`)
waits until the members joined or `GroupWait` (3 s) passed, takes a free cat
slot per member without waiting for one while holding another, reads the file
once from its beginning and delivers every line (with its newline, empty lines
included) to every member, which numbers from 1 and filters on its own. This
is a snapshot read: delivery blocks for a slow member instead of evicting it,
there is no join skip and no held descriptor; a cancelled member leaves and
releases the group. A member arriving after the read started, beyond
`MaxConcurrentCats` members, without a free slot, or within 10 minutes after
the group's read ended reads privately. The follow-only hub behaviour
(eviction, join skip, held descriptors, read positions, in-order long line
warnings) is wired through optional interfaces the fan-out processor
type-asserts (`readTracker`, `warningSource`), which only the follow entry
implements.
