# Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Command reads local files instead of the fleet | No `--servers`: serverless mode | Pass `--servers HOSTS` or a server list file. |
| `dmap` waits for input / shows `query>` | Started without `--query` on a terminal: query builder | Always pass `--query`; add `--no-interactive`. |
| `dmap` exits 1: `no mapreduce query specified` | No query, no terminal | Pass `--query '...'`. |
| `dtail` never ends | Follow mode | `--shutdownAfter SECONDS`, or use `dgrep`/`dcat` for past data. |
| No progress shown, or final line `reporting=0/N` | `--quiet`/`--plain` drop the STATS fields; dservers older than the progress report send none; `dtail` follow and `journal:`/stdin reads are not counted | Add `--progress` (stderr); upgrade dserver. |
| Interactive prompt/pause in an agent run | `dtail` is interactive on a terminal | `--no-interactive`, or run without a TTY. |
| Query result is one empty group, or no rows | A bareword written as `$field` (no value; the client warns `... is not a known variable ... did you mean bareword ...`) | Write dynamic `key=value` fields without `$`. |
| Query sees no fields / only `$line`, `$hostname` | No `from TABLE`: the `generic` parser is used | Add `from TABLE` (e.g. `from STATS`) or `logformat generickv`/`csv`. |
| `from .` or `from *` on an application log returns nothing | They keep the `default` parser, which reads only DTail `MAPREDUCE` lines | Drop `from`, or add `logformat generickv`/`generic`/`csv`. |
| Only 10 rows shown, WARN `Got N results but limited terminal output to 10 rows!` | The printed result (stdout, piped or not) is capped at 10 rows without `limit` | Add `limit N` or write an `outfile`. |
| `where a ne x` drops lines | Conditions on a missing field are false | Expected; there is no `or`. |
| `from stats` finds nothing although lines say `MAPREDUCE:stats` | Table names are upper-cased; a lower-case table is in no named table | Use `from *` or `from .` (without `from` the `generic` parser sees no `key=value` fields). |
| Numeric aggregate ignores lines | Only lines with a numeric value for the field count | Check sample lines with `dgrep`. |
| `HOST KEY CHANGED` | Server key differs from `known_hosts` (reinstall or MITM) | Verify, then remove the entry or add `--trustChangedHostKeys`. |
| Unknown-host question blocks a script | Fresh `known_hosts` | `--trustAllHosts` only if the user accepts it. |
| `no handshake within 30s` | Host accepts TCP but SSH stalls, or a touch-to-sign key waits | Check the host; confirm the key promptly or raise `Common.SSHHandshakeTimeoutMs`. |
| Auth fails though `ssh` works | Wrong user/key; server reads another `authorized_keys` or key cache | `--user`, `--auth-key-path`, check `Server.AuthorizedKeysPath`/key cache on the server. |
| `unsupported DTail server: not a protocol 5 server` | dserver older than the client | Upgrade dserver. |
| `dcat` hangs after a version mismatch error | Client older than dserver | Upgrade the client. |
| Journal target rejected (`journal-v1`) | Server not Linux, or no `journalctl` on its `PATH` | Read files instead, or fix the server. |
| `read: unable to read file` on `journal:x` | Unit name without type, glob, slice or template | Use a literal unit with type, e.g. `journal:sshd.service`. |
| Exit `2`, `read: no permission to read file` | `Server.Permissions` deny (deny rules win) or OS permissions; or a path with a newline | Ask the server admin; see [dserver-config.md](dserver-config.md#read-permissions). |
| Exit `2`, `read: no file to read` | Glob matched nothing, file missing or not regular | Check the path on each host. |
| `read: more files than the server reads` | Glob above `Server.MaxGlobTargets` (1000) | Narrow the glob. |
| `output: too large for the session output buffer` | Lines larger than the server's output buffer | Raise `Server.OutputBufferMaxBytes` or lower `MaxLineLength`. |
| Lines split with `Long log line` warning | Line above `Server.MaxLineLength` (1 MiB) | Expected; raise the limit if needed. |
| `dmap` WARN `The mapreduce result may be incomplete` | A server failed or a session ended early | Treat the result as partial; rerun or report which servers. |
| Connections refused under load | `Server.MaxConnections` (10) reached | Raise it on the server, or fewer concurrent clients. |
| `dcat`/`dgrep` slow on many sessions | `Server.MaxConcurrentCats` (2) one-shot read slots | Raise it on the server. |
| `.zst` file unreadable | Build without zstd (`DTAIL_NO_ZSTD=yes`) | Use a build with zstd. |
| `dtailhealth` says WARNING | No `--server` given: serverless check | Pass `--server HOST:PORT`. |

When diagnosing, reproduce on one host with a narrow file first, read the
client log (`~/log/YYYYMMDD.log`) and the dserver log (its `LogDir`, default
`log/` under its working directory, or the systemd journal).
