Exit statuses of `dcat` and `dgrep`
===================================

`dcat` and `dgrep` exit with one of three statuses, in server and in
serverless mode alike. With several servers the client exits with the highest
status of them all.

| Status | Meaning |
|---|---|
| `0` | Every server read what was asked. `dgrep` exits `0` also when no line matched (unlike `grep`, which exits `1` then). |
| `1` | A connection or session failed, or the client could not start. See [Status 1](#status-1-connection-and-session-failures). |
| `2` | A server reported a failed command, or the command line had an invalid flag. See [Status 2](#status-2-failed-commands). |

```shell
% dcat --servers serverlist.txt --files /var/log/missing.log; echo $?
...
2
```

## Status 1: connection and session failures

The client exits `1` when:

* a server could not be connected to (refused or unreachable);
* the SSH handshake failed, or no SSH session could be opened;
* the server rejected or did not acknowledge an `--interactive-query` session
  start, or the commands could not be sent;
* a server does not support a requested `journal:` target (capability
  `journal-v1`);
* the server is of another protocol version (see
  [Protocol versions](#protocol-versions));
* a serverless session failed;
* a session was cut short in the middle of a read (see
  [Sessions cut short](#sessions-cut-short));
* the client could not start, e.g. a configuration error or no server to
  connect to.

## Status 2: failed commands

A server reports a failed command when:

* a file is missing, also a glob that matched no file after its retries;
* a file is denied by the read permissions;
* a file could not be opened or read;
* a glob matched more files than the server reads (`Server.MaxGlobTargets`);
* the server could not parse the read command;
* a read produced more output than the session output buffer holds
  (`Server.OutputBufferMaxBytes`).

The output of the other files and servers is still printed. The reason is in
the server's warnings and in its log. Both settings are described in the
[Server Configuration Guide](./server-configuration.md).

A read failure on one server (`2`) outweighs a connection failure on another
(`1`), as the highest status wins.

### Failed commands in an `--interactive-query` session

* A failure that reached the client counts for the whole session. That holds
  also when it was reported for an earlier workload that a `:reload` has
  replaced since.
* A failure of a replaced workload that `dserver` still had queued when the
  reload took effect is dropped with that workload's other output. It does
  not count.
* A workload that a reload cancelled reports no further failures.

## Sessions cut short

A session is cut short when it ends without the server's close handshake:
`dserver` was killed, crashed or shut down, or the network failed.

* The client exits `1`, as the output may be incomplete.
* It logs `Session ended without the server's close handshake, the output may
  be incomplete` for that server.
* A failed command the server reported before (`2`) still outweighs it.
* Every `dserver` the client accepts (protocol 5) ends a session it completed
  with that handshake.
* A read the client stopped itself keeps its status: Ctrl+C twice, `SIGTERM`,
  or `:quit` in an `--interactive-query` session.

## Protocol versions

Clients and servers must both speak protocol 5 (`--version` prints it).

**Older server.** The client reports `unsupported DTail server: not a protocol
5 server (no capabilities frame first); please update DTail server` and exits
`1`. Some protocol 4.1 servers close on the client's control commands before
reading files, which would otherwise look like a successful empty result.

**Older client** (protocol 4.x or lower) against a protocol 5 `dserver`:

* It gets `... does not match client protocol version '...', please update
  DTail client`, possibly after some unreadable output.
* `dserver` keeps the session open, so that an older `dtail` does not
  reconnect in a loop.
* The client therefore waits until it is stopped (Ctrl-C) or the server's
  `Server.IdleSessionTimeoutS` ends the session.

## Log lines that look like control messages

In `--plain` server mode a log line that starts with `.syn command failed`,
or a log line `.syn close connection`, is printed like any other line. It
neither changes the exit status nor ends the read: the server marks its own
control messages, so no log line can be taken for one.

## The other clients

| Client | Exit status |
|---|---|
| `dtail` (follow mode) | Not affected by failed reads or sessions cut short; it reconnects. |
| `dmap` | Keeps its status. Its scheduled modes handle failures, see the [Server Configuration Guide](./server-configuration.md). |
| `dtailhealth` | Has its own statuses. |

See also the [examples](./examples.md).
