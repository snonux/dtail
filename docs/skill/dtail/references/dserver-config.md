# dserver configuration

One JSON file (`-cfg`, usually `/etc/dserver/dtail.json`) with `Common`,
`Server` and `Client` sections; dserver uses `Common` and `Server`. Unknown
keys are ignored (old `Turbo*` keys included). Schema:
`examples/dtail.schema.json` (kept in sync by a unit test); commented
example: `examples/dtail.json.example`. Full guide:
`doc/server-configuration.md`.

## Read permissions

```json
"Permissions": {
  "Default": ["readfiles:^/var/log/.*$", "readfiles:!^/var/log/secret/"],
  "Users": {
    "alice": ["readfiles:^/var/log/.*$", "readfiles:^journal:.*\\.service$"]
  }
}
```

- **The built-in default is `["^/.*"]`: every authenticated user may read
  every file the OS lets them.** Set `Default` explicitly.
- `Users[name]` replaces `Default` for that user; `Default` stays `^/.*`
  when left out, even with `Users` set. An empty `Default` list lets users
  without an entry read nothing.
- Rule `readfiles:REGEX` (prefix optional) allows; `readfiles:!REGEX`
  denies. **Any matching deny rule wins**, regardless of order; no match =
  denied.
- Matched against the resolved absolute path (symlinks resolved). On
  case-insensitive file systems write `(?i)` deny rules.
- Paths holding a newline are never readable.
- Journal targets match the full string: `^journal:nginx\.service$`.
  Deny the canonical unit name, not an alias.
- OS permissions apply too: the user needs read access (with an ACL build),
  or the `dserver` user does (without).
- To hide a directory from path completion, deny the directory path itself:
  `!^/var/log/secret/` (or `.../.*$`). `Server.PathCompletionDisable: true`
  turns listings off.
- dserver's own jobs bypass these rules.

## Key settings (defaults)

| Key | Default | Notes |
| --- | --- | --- |
| `Common.SSHPort` | 2222 | |
| `Common.LogDir`, `Common.Logger`, `Common.LogLevel` | `log`, `file`, `info` | flags win |
| `Common.CacheDir` | `cache` | |
| `Common.SSHConnectTimeoutMs` / `SSHHandshakeTimeoutMs` | 2000 / 30000 | also for jobs |
| `Server.SSHBindAddress` | `0.0.0.0` | |
| `Server.HostKeyPath`, `HostKeyBits` | `./cache/ssh_host_key`, 4096 | generated if missing |
| `Server.AuthorizedKeysPath` | empty | one file for all users |
| `Server.KeyExchanges`, `Ciphers`, `MACs` | empty | restrict SSH algorithms |
| `Server.MaxConnections` | 10 | handshakes count (10 s server handshake limit) |
| `Server.MaxConcurrentCats` | 2 | one-shot reads (dcat/dgrep/dmap) |
| `Server.MaxConcurrentTails` | 50 | follow reads |
| `Server.IdleSessionTimeoutS` | 900 | rolling; closes ~960-990 s after last activity |
| `Server.MaxLineLength` | 1048576 | longer lines split with a warning |
| `Server.MaxGlobTargets` | 1000 | glob cap per read |
| `Server.MaxCommandFrameSize` | 1048576 | |
| `Server.ReadGlobRetryIntervalMs`, `ReadRetryIntervalMs` | 5000, 2000 | |
| `Server.OutputBufferMaxBytes` | 2097152 | >= `MaxLineLength` + 128 KiB or dserver refuses to start |
| `Server.OutputFlushTimeoutMs`, `OutputEOFAckTimeoutMs` | 2000, 2000 | stall timeouts |
| `Server.OutputReadRetryIntervalMs` | 1 | compatibility fallback |
| `Server.SharedReadsDisable` | false | |
| `Server.PathCompletionDisable` | false | |
| `Server.MapreduceLogFormat` | `default` | parser for queries with `from` |
| `Server.AuthKeyEnabled`, `AuthKeyTTLSeconds`, `AuthKeyMaxPerUser` | true, 86400, 5 | fast reconnect |
| `Server.Schedule`, `Server.Continuous` | empty | see [dserver-jobs.md](dserver-jobs.md) |

## Behaviours worth knowing

- **Shared reads** (on by default): sessions following the same
  uncompressed file share one reader; each still checks permissions and
  filters itself. A lagging session is evicted to a private reader and
  rejoins later; output equals non-shared reads. Scheduled jobs of one
  wave reading the same files share a one-shot read.
- **Rotation:** follow reads drain the old file before switching to the
  new one (re-polls it every 100 ms while lines keep arriving, at most 10
  polls).
- **Journal:** `journal-v1` is advertised only on Linux with `journalctl`
  on `PATH`; the unit list for path completion is cached 60 s.
- **Output limits:** a slow or paused client only applies backpressure;
  output that cannot fit the session buffer is reported
  (`output: too large for the session output buffer`), never silently
  dropped.
- **Probes** (interactive field discovery and path listings) obey the same
  permissions, are bounded (8 files, 10 s, 48 KiB) and log one INFO line
  each.
