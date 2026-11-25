DTail Server Configuration Guide
================================

How to configure `dserver`, the DTail server daemon: every key with its
default, and short examples. Details live in:

* [Read permissions](./read-permissions.md)
* [Scheduled and continuous jobs](./scheduled-jobs.md)
* [Auth-key fast reconnect](./auth-key-fast-reconnect.md)

## The configuration file

`dserver` reads one JSON file:

| Started with | File read |
| --- | --- |
| `-cfg PATH` | Exactly that file. The example systemd unit passes `-cfg /etc/dserver/dtail.json`. |
| no `-cfg` | `~/.config/dtail/dtail.conf` if it exists, else `~/.dtail.conf`. Never both. |
| `-cfg none` | None. |

The file has the sections `Common`, `Server` and `Client`; `dserver` uses
`Common` and `Server`. Key names are matched case-insensitively. Unknown
keys, e.g. from older releases, are ignored.

* Commented example: [examples/dtail.json.example](../examples/dtail.json.example)
* JSON schema of every key: [examples/dtail.schema.json](../examples/dtail.schema.json)
  (a unit test keeps it in sync with the code)

`dserver` refuses to start as root (UID or GID 0); run it as an unprivileged
user such as `dserver`.

### Command-line flags

Flags override the configuration file.

| Flag | Overrides | Meaning |
| --- | --- | --- |
| `-cfg PATH` | | Configuration file (`none`: read none). |
| `-port N` | `Common.SSHPort` | SSH port (default `2222`). |
| `-bindAddress ADDR` | `Server.SSHBindAddress` | Address to listen on. |
| `-host-key-path PATH` | `Server.HostKeyPath` | Private SSH host key. |
| `-authorized-keys-path PATH` | `Server.AuthorizedKeysPath` | One `authorized_keys` file for all users. |
| `-logDir DIR` | `Common.LogDir` | Log directory. |
| `-logger NAME` | `Common.Logger` | Logger (`file`, `stdout`, `fout`, `none`). |
| `-logLevel LEVEL` | `Common.LogLevel` | Log level (default `info`). |
| `-hostname-override NAME` | `Common.HostnameOverride` | Hostname in logs and output (also `DTAIL_HOSTNAME_OVERRIDE`). |
| `-color` | | Colored log output. |
| `-pprof ADDR` | | Start a pprof HTTP server at this address. |
| `-shutdownAfter N` | | Shut down after N seconds (testing). |
| `-version` | | Print the version and exit. |

## Read permissions

`Server.Permissions` controls which files each SSH user may read:

```json
"Permissions": {
  "Default": [
    "readfiles:^/var/log/.*$"
  ],
  "Users": {
    "jamesblake": [
      "readfiles:!^/var/log/secret/.*$",
      "readfiles:^/var/log/.*$"
    ]
  }
}
```

| Key | Default | Meaning |
| --- | --- | --- |
| `Permissions.Default` | `["^/.*"]` | Rules of every user without an own entry. The default allows every file the OS lets the user read, so always set it. |
| `Permissions.Users` | *(none)* | User name to rule list; it replaces `Default` for that user. |

The rules in short:

* A rule is `[readfiles:][!]<regex>` (Go `regexp`, unanchored unless it uses
  `^`/`$`). `!` makes it a deny rule.
* **Deny wins**: a matching deny rule rejects the path wherever it stands in
  the list. A path no allow rule matches is denied.
* The regex is matched against the resolved absolute path (symlinks
  resolved).
* A `journal:` target must be matched as a whole:
  `"readfiles:^journal:.*\\.service$"`.
* The OS file permissions apply as well.
* `dserver`'s own jobs bypass the rules.
* A user with an empty rule list gets no session at all.

Everything else (case-insensitive file systems, paths with a newline, which
directories the interactive path completion lists) is in
[Read permissions](./read-permissions.md).

## Connection and session limits

SSH server:

| Key | Default | Meaning |
| --- | --- | --- |
| `Server.SSHBindAddress` | `0.0.0.0` | Address the SSH server binds to. |
| `Common.SSHPort` | `2222` | Port the SSH server listens on. |
| `Server.HostKeyPath` | `./cache/ssh_host_key` | Private SSH host key, generated if missing. The legacy key `Server.HostKeyFile` is still accepted. |
| `Server.HostKeyBits` | `4096` | RSA key size of a generated host key (`0` or less: `4096`). |
| `Server.AuthorizedKeysPath` | *(empty)* | One `authorized_keys` file for all users. Empty: see below. |
| `Server.KeyExchanges`, `Server.Ciphers`, `Server.MACs` | *(empty)* | Restrict the allowed SSH algorithms. |

With an empty `AuthorizedKeysPath` a user's keys are read from
`<CacheDir>/<user>.authorized_keys` if that file exists (see
`examples/update_key_cache.sh.example`), else from the user's
`~/.ssh/authorized_keys`. A relative `CacheDir` is relative to the working
directory.

Limits:

| Key | Default | Meaning |
| --- | --- | --- |
| `Server.MaxConnections` | `10` | Concurrent connections, those still in their SSH handshake included. |
| `Server.MaxConcurrentCats` | `2` | Concurrent one-shot reads (`dcat`, `dgrep`, `dmap`); further reads wait. At least `1`. |
| `Server.MaxConcurrentTails` | `50` | Concurrent follow reads (`dtail`, continuous jobs); further reads wait. At least `1`. |
| `Server.IdleSessionTimeoutS` | `900` | Seconds without SSH activity after which a connection is closed (`0` or less: `900`). |
| `Server.MaxLineLength` | `1048576` | Bytes after which a line, or a journal entry, is split into several. |
| `Server.MaxGlobTargets` | `1000` | Files one glob may expand to (`0` or less: `1000`). |
| `Server.MaxCommandFrameSize` | `1048576` | Bytes of one command frame (`0` or less: no limit). |
| `Server.ReadGlobRetryIntervalMs` | `5000` | Retry interval while a glob matches no file yet. |
| `Server.ReadRetryIntervalMs` | `2000` | Retry interval for reading a file again in tail/cat loops. |

Notes:

* **Handshake:** a client must complete the SSH handshake within 10 seconds
  (fixed).
* **`MaxConcurrentCats`** also bounds how many scheduled jobs run together
  (see "Shared reads").
* **`IdleSessionTimeoutS`** is a rolling timeout: every read or write counts
  as activity, so active follow sessions stay connected. With the
  default an idle connection is closed 960-990 s after its last activity.
  Values below about a minute are not useful.
* **`MaxGlobTargets`:** excess paths are dropped and the client is told
  `read: more files than the server reads`.
* **`MaxCommandFrameSize`:** a larger frame is rejected and the session
  closed. Health-check sessions always keep the 1 MiB limit.

### Line length and memory

`MaxLineLength` counts a line's bytes without its `\n`, but with the `\r` of
a CRLF ending.

* A line up to the limit is read whole. A longer one is cut into pieces of
  exactly the limit and the client gets a long-line warning. This holds for
  follow and one-shot reads, files and `journal:` entries alike.
* A long journal entry does not end the read. Only an unterminated last
  entry, which a stopped `journalctl` can leave, is dropped.
* `Server.OutputBufferMaxBytes` must be at least `MaxLineLength` + 128 KiB,
  or `dserver` refuses to start.

Memory per concurrent read:

| Read | Buffers held |
| --- | --- |
| One-shot file read | One pooled buffer of 1 MiB + 1 byte. |
| `journal:` read | Two such buffers for the whole `journalctl` run (stdout and stderr). |
| Follow file read | The unfinished line, up to `MaxLineLength`, in a growing buffer. |
| Any read of a line over 1 MiB (limit above 1 MiB only) | A buffer of its own beside the pooled one, grown by doubling to about twice the longest line, capped at `MaxLineLength` + 1 bytes. While it grows, the old and the new buffer exist together (up to about 1.5 times the final size). |

A `journalctl` stderr line over 1 MiB reaches the client cut to 1 MiB, with
a note.

## Output path limits

These bound the memory of one session's pending output and the time
`dserver` waits for a slow client:

| Key | Default | Meaning |
| --- | --- | --- |
| `Server.OutputBufferMaxBytes` | `2097152` (2 MiB) | Output memory kept per session while the client is slow; readers are slowed down at the cap. |
| `Server.OutputFlushTimeoutMs` | `2000` | Time the client may take nothing while output is drained. |
| `Server.OutputReadRetryIntervalMs` | `1` | Compatibility fallback interval; too small values are clamped. Leave it alone. |
| `Server.OutputEOFAckTimeoutMs` | `2000` | Time to wait for the client's acknowledgement of the end of the output. |

Notes:

* `OutputBufferMaxBytes` must be at least `MaxLineLength` + 131072, or
  `dserver` refuses to start.
* Each session raises its own cap to at least 2 × `MaxLineLength` + 131072
  (2228224 bytes with the defaults), as escaping can almost double a line
  on the wire. The configured value need not cover that.
* `OutputFlushTimeoutMs` is a stall timeout. A client that keeps reading,
  however slowly, or that paused while `dserver` is still writing to it (a
  pager, a stopped pipeline) is waited for; `IdleSessionTimeoutS` bounds a
  client that never comes back.

## Other server settings

| Key | Default | Meaning |
| --- | --- | --- |
| `Server.MapreduceLogFormat` | `default` | Log format of MapReduce queries with a `from` clause and without a `logformat` clause; queries without `from` use `generic` (see [Log formats](./logformats.md)). |
| `Server.SharedReadsDisable` | `false` | Turn shared reads off (see "Shared reads"). |
| `Server.PathCompletionDisable` | `false` | Turn off the path listing of the clients' interactive path completion (answered `probe: path listing disabled`). |

## Common settings and logging

| Key | Default | Meaning |
| --- | --- | --- |
| `Common.SSHPort` | `2222` | Port `dserver` listens on and clients (and jobs) connect to. |
| `Common.LogDir` | `log` | Log directory, relative to the working directory unless absolute; `~/` is expanded. (Clients: `~/log`.) |
| `Common.Logger` | `file` | `file`, `stdout`, `fout` (both) or `none`, case-insensitive. (Clients: `fout`.) |
| `Common.LogLevel` | `info` | `none`, `fatal`, `error`, `warn`, `info`, `default`, `verbose`, `debug`, `devel`, `trace` or `all`, case-insensitive. |
| `Common.LogRotation` | `daily` | `daily`: `<LogDir>/YYYYMMDD.log`. Any other value (e.g. `signal`): `<LogDir>/dserver.log`. |
| `Common.CacheDir` | `cache` | Directory of the per-user `<user>.authorized_keys` key cache. |
| `Common.HostnameOverride` | *(empty)* | Hostname in logs and output instead of the system's. |
| `Common.SSHConnectTimeoutMs` | `2000` | TCP connect timeout of the connections of `dserver`'s jobs (and of the clients). |
| `Common.SSHHandshakeTimeoutMs` | `30000` | SSH handshake timeout after the TCP connect, likewise. |
| `Common.ExperimentalFeaturesEnable` | `false` | Experimental features (development only). |

Notes:

* **Rotation:** SIGHUP reopens the log file in both modes, so with a value
  other than `daily` an external tool such as `logrotate` can rotate
  `dserver.log`. `dserver` never deletes old log files; see
  `examples/prune_dserver_logs.sh.example`.
* **Log level and clients:** the level only decides what `dserver` writes to
  its own log. The warnings and errors it sends to clients (a missing file,
  a glob matching too many files, a split long line) are sent at every
  level, also `none`. How clients show them: [examples.md](./examples.md).
* **One line per log line:** control and format characters in logged text
  (a path, glob or error a client sent) are written as Go escapes (`\n`,
  `\t`, `\x1b`, `‮`, ...), so a client can forge neither log lines nor
  terminal escapes. Other text, non-ASCII names included, is unchanged. The
  same holds for the client logs and the warnings sent to clients.

## Auth-key fast reconnect

A client can register a public key with `dserver` over an authenticated
session; later connections authenticate against that in-memory key, without
`authorized_keys` or a YubiKey touch.

| Key | Default | Meaning |
| --- | --- | --- |
| `Server.AuthKeyEnabled` | `true` | Accept key registrations and cached keys. |
| `Server.AuthKeyTTLSeconds` | `86400` | Lifetime of a cached key in seconds (`0` or less: the default). |
| `Server.AuthKeyMaxPerUser` | `5` | Cached keys per user (`0` or less: the default). |

Usage and security notes:
[Auth-Key Fast Reconnect](./auth-key-fast-reconnect.md).

## Journal reads

On Linux hosts with systemd, clients can read a unit's journal instead of a
file:

```shell
dcat --servers server1 --files 'journal:nginx.service'
```

| Topic | Behaviour |
| --- | --- |
| Availability | Only on Linux with `journalctl` on `PATH`; `dserver` then advertises the capability `journal-v1`, and clients reject journal targets without it. |
| One-shot read (`dcat`, `dgrep`, `dmap`) | Reads the current journal once. |
| Follow read (`dtail`) | Runs `journalctl -f -n 0` and restarts it until the client disconnects. |
| Lines | One entry is one line, cut at `Server.MaxLineLength` like a file line. |
| Unit name | One literal unit with its type: `journal:sshd.service`, not `journal:sshd`, a glob, a slice or a template. Others fail with `read: unable to read file`, also in job `Files`. |
| Permissions | A rule must match the whole target, e.g. `"readfiles:^journal:.*\\.service$"`. |

What exactly is refused, and what a deny rule on a unit does not cover
(other units' messages about it, aliases), is in
[Read permissions](./read-permissions.md#journal-targets).

## Shared reads

On by default; `Server.SharedReadsDisable: true` turns them off.

* Sessions that follow (tail) the same uncompressed file share one reader of
  it. Each session still passes its own permission check and filters and
  numbers its lines itself.
* A session that falls too far behind gets a reader of its own and rejoins
  the shared one once it caught up.
* Scheduled jobs that the scheduler starts together on the same files of the
  same `dserver` share a one-shot read the same way.

## Scheduled and continuous jobs

`dserver` can run MapReduce jobs by itself. Both arrays are in the `Server`
section:

```json
"Schedule": [
  {
    "Name": "daily-errors",
    "Enable": true,
    "Files": "/var/log/app/app-$today.log",
    "Query": "select count(*)",
    "Outfile": "out/daily-errors-$today.map",
    "Servers": ["localhost"],
    "AllowFrom": ["localhost"],
    "TimeRange": [9, 17]
  }
],
"Continuous": [
  {
    "Name": "live-errors",
    "Enable": true,
    "Files": "/var/log/app/errors.log",
    "Query": "select count(*)",
    "Outfile": "out/live-errors.map",
    "AllowFrom": ["localhost"],
    "RestartOnDayChange": true
  }
]
```

| Field | Applies to | Meaning |
| --- | --- | --- |
| `Name` | both | Job name; also the job's password, so keep it unguessable. |
| `Enable` | both | `true` to run the job. |
| `Files` | both | Comma-separated files or globs. Date placeholders allowed. |
| `Query` | both | MapReduce [query](./querylanguage.md) without an `outfile` clause. |
| `Outfile` | both | Result file (CSV). Date placeholders allowed. A scheduled job does not run while it exists. |
| `Servers` | both | Servers to query, or one server list file. Empty: this `dserver`. |
| `Discovery` | both | Optional: `COMMA` or `FILE`, how `Servers` is read. |
| `AllowFrom` | both | Addresses the job's connections are accepted from. Empty accepts none. |
| `TimeRange` | `Schedule` | `[startHour, endHour]`: runs while `startHour <= hour < endHour`. `[0, 24]` is the whole day; without it the job never runs. |
| `RestartOnDayChange` | `Continuous` | Restart at midnight with the new dates. |

Date placeholders (as `YYYYMMDD`, local time): `$today`, `$tomorrow`,
`$yesterday`, `$yyesterday`, `$yyyesterday`.

The essentials:

* Every `dserver` a job connects to needs the same job entry (type and
  `Name`) with the scheduling server's address in `AllowFrom`.
* The scheduler checks its jobs every minute.
* A scheduled job writes its outfile only when its run completed. A failed
  run writes nothing and is retried with a growing backoff (1 minute up to
  1 hour).
* After its `TimeRange` ended, a job that failed only because files could
  not be read writes what it could read. After 24 more hours the scheduler
  gives up.
* A continuous job follows its files like `dtail`, writes a result every
  query `interval`, and is started again a minute after it exited.

Server lists, authentication, the failure kinds, final runs and log lines:
[Scheduled and continuous jobs](./scheduled-jobs.md).

## A minimal example configuration

```json
{
  "Common": {
    "SSHPort": 2222,
    "LogDir": "log",
    "CacheDir": "cache"
  },
  "Server": {
    "SSHBindAddress": "0.0.0.0",
    "HostKeyPath": "cache/ssh_host_key",
    "MaxConnections": 50,
    "MaxConcurrentCats": 8,
    "MaxConcurrentTails": 100,
    "IdleSessionTimeoutS": 900,
    "MapreduceLogFormat": "default",
    "AuthKeyEnabled": true,
    "AuthKeyTTLSeconds": 86400,
    "AuthKeyMaxPerUser": 5,
    "Permissions": {
      "Default": [
        "readfiles:^/var/log/.*$",
        "readfiles:!^/var/log/secret/.*$"
      ],
      "Users": {
        "alice": [
          "readfiles:^/var/log/.*$",
          "readfiles:^journal:.*\\.service$"
        ]
      }
    },
    "Schedule": [
      {
        "Name": "daily-nginx-errors",
        "Enable": true,
        "Files": "/var/log/nginx/access.log-$today",
        "Query": "select count(*)",
        "Outfile": "out/nginx-errors-$today.map",
        "AllowFrom": ["localhost"],
        "TimeRange": [0, 24]
      }
    ]
  }
}
```
