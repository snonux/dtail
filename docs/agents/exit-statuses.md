# Client Exit Statuses (dcat, dgrep)

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

`dcat` and `dgrep` exit `0` on success (`dgrep` also when no line matched, unlike
`grep`), `1` when a server could not be connected to (refused or unreachable),
the SSH handshake failed or no SSH session could be opened, the server rejected
or did not acknowledge an `--interactive-query` `SESSION START` or the commands
could not be sent, a server lacks `journal-v1` for a requested journal target,
the server is of another protocol version (see "Wire Framing"), a serverless
session failed, a session was cut short (below), or the
client could not start, and `2`
(`clients.ExitStatusFailedCommand`, also the status of an invalid flag) when any
server reported a failed command of the session with `.syn command failed
<reason>` (every protocol 5 dserver, `handlers.SessionOutcome.Failures`).
This covers the file read failures (`protocol.IsFileReadFailure`: missing, not
permitted, unreadable, reader not created, glob capped) and deliberately also the
other reasons (`command: ...`, `map: invalid query`, `read: unable to parse
command`, `output: too large for the session output buffer`), as each means
output that was asked for is missing. Serverless mode behaves the same, as its
in-process server reports failures the same way. The client exits with the highest
status of its connections (`baseClient.runConnections`), so a read failure on
one server outweighs a connection failure on another. The raise is opt-in per
client (`clientProfile.failedCommandStatus`, set by `readClientProfile`):
`dtail` follow mode, `dmap` (its scheduled modes handle failures, see "Failed
scheduled jobs" below) and `dtailhealth` keep their statuses; interactive
`dmap` thus exits `0` and writes its outfile also after a reported `map:
incomplete result`, which dserver only sends when a final result was cut off
(see "Server-Side MapReduce" below), and also after a session cut short
(below; `dmap` does not set `cutShortStatus` either), e.g. by the idle
timeout, a dserver SIGTERM or a lost connection while it sent the final
result, which then has no report. This is by design, so that it always
writes what it received; instead it logs every reported failure that is no
file read failure, and every session cut short that the client did not end
itself, at WARN first (`The mapreduce result may be incomplete|<server>:
<reason>; ...`, the reason for a session cut short being `the session ended
before the server completed it`, `MaprClient.incompleteResultReasons` called
by `MaprClient.finish`). Whether the client ended a session is decided per
session at the moment it ended (`baseClient.runSession` records it next to the
connection, reset by `replaceConnection`): it did if the context of
`runConnections` was done by then, which SIGINT/SIGTERM and an
`--interactive-query` `:quit` or control TTY end cancel. So `:quit` warns
about nothing, while a server that went away mid-run is still warned about
when the user presses Ctrl-C only later.
A session cut short (it ended without `.syn close connection`,
`handlers.SessionOutcome.Completed` false: e.g. a dserver SIGKILLed, crashed or
stopped mid-read, or a lost connection) exits `1` (`clients.exitStatusCutShort`
via `clientProfile.cutShortStatus`, which only `readClientProfile` sets), and the
client logs `Session ended without the server's close handshake, the output may
be incomplete` for that server, unless the connection already reported an
error (handler status above 0; client handlers start at `-1`). A session the
client ended itself keeps its status: `baseClient.runSession` checks, when the
session ended, the context of all connections (not the per-connection
`connCtx`, which the connector cancels itself when its session ends), which
SIGINT/SIGTERM (cancel of `Client.Start`'s context) and an `--interactive-query`
`:quit` or control TTY end (cancel of `runConnections`' context) cancel, and
passes the result to `baseClient.sessionStatus`. A
reported failed command (`2`) outweighs it. Every protocol 5 server (also the
in-process server of serverless mode) sends the close handshake after a
completed session's output, and a server of another protocol version is
rejected (see "Wire Framing"), so no capability gates this;
`--max` stops on the server, which then closes normally. A session whose
frames are of another protocol version is no completed one: dserver sends the
mismatch error once (`handlers.commandDispatcher.rejectProtocolVersion`).
How the session goes on depends on the kind of client
(`handlers.mismatchedClient`, `internal/handlers/protocol_codec.go`):
- An older client (protocol major version <= 4, an unparsable version, or no
  `protocol` prefix at all, as v1.x clients send `%s;` frames) keeps its
  session, until the client exits or `IdleSessionTimeoutS`
  (`ServerHandler.versionRejected`). Clients that retry reconnect a few
  seconds after a session ended (v1.x-v3 `dtail`, v3 `dmap` in tail mode
  without outfile after 2 s), so ending it would make them reconnect in a
  loop with full SSH handshakes and server log lines. Observed with 4.1
  builds of this fork from before protocol 5 (3ea4784) against a protocol 5
  dserver that ended the session: their `dtail` reconnected about every 3 s
  until stopped (their `dcat` and `dmap` exited `1`). What holds for every
  such client, released 4.x and <= 3 ones included: it gets the mismatch
  error (`... does not match client protocol version '...', please update
  DTail client`), possibly after unreadable output (the capabilities frame
  is sent in protocol 5 framing before dserver knows the client's version),
  and its session stays open until the client exits or
  `IdleSessionTimeoutS`, so a one-shot client such as `dcat` waits until it
  is stopped. The pre-protocol-5 fork builds above also report
  `unsupported DTail server: missing capability escaped-framing-v1`;
  released 4.x clients (e.g. v4.3.4) predate that check.
- The health handler keeps only the sessions of clients of protocol 3 and
  lower (and unparsable or missing versions) open
  (`HealthHandler.versionRejected`): `dtailhealth` does not reconnect, so a
  4.x `dtailhealth` gets its session ended at once and its check fails.
- Any other version (e.g. a future one, or another 5.x minor version) gets
  the session ended without the close handshake
  (`handlers.baseHandler.closeWithoutHandshake`), as such a client may not
  recognize or acknowledge it, so it sees the error and the session ends at
  once instead of after the 5 s acknowledgement timeout. Clients that check
  for the close handshake treat that session as cut short (`dcat`/`dgrep`
  exit `1`, a scheduled job counts a transport failure).
In `--plain` server mode a payload line starting with `.syn command failed`
or a payload line `.syn close connection` is payload (printed, exit
unchanged), as every control frame is marked (see "Wire Framing" above). With
`--interactive-query` the client keeps the failures of every workload of the
session that reached it, also of workloads a `:reload` replaced; dserver drops
queued messages of a stale generation (`outputCoordinator.shouldDropGeneration`),
failure reports included, and a cancelled workload reports none
(`readCommand.reportFailure`), so those do not count.
User docs: `doc/examples.md` ("Exit statuses of `dcat` and `dgrep`").
