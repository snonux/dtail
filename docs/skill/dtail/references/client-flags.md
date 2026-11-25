# Client flags

Flags take one or two dashes (`-files` = `--files`). Check a deployed
binary with `-h` and `--version` if it may be older.

## Per command

| Command | Flags of its own (besides the shared ones below) |
| --- | --- |
| `dcat` | none |
| `dgrep` | `--regex RE` (default `.`), `--grep RE` (alias), `--invert`, `--before N`, `--after N`, `--max N` |
| `dtail` | the `dgrep` filter flags, `--query Q`, `--timeout N`, `--shutdownAfter N` (seconds, default 86400), `--no-interactive`, `--colorTable`, `--wideColorTable`, `--checkHealth` (deprecated, use `dtailhealth`) |
| `dmap` | `--query Q`, `--timeout N`, `--no-interactive` |
| `dtailhealth` | `--server HOST[:PORT]`, `--logger` (default none), `--logLevel` (default none), `--hostname-override`, `--trustChangedHostKeys`, `--no-auth-key` (no effect), `--pprof`, `--version`. No `--cfg`, no `--auth-key-path`, none of the shared flags. |

## Shared by dcat, dgrep, dtail, dmap

| Flag | Meaning |
| --- | --- |
| `--servers`, `--discovery`, `--port` (2222, for servers given without one), `--user`, `--cpc` (10) | Server selection; see [servers-and-sources.md](servers-and-sources.md). |
| `--files` | Comma list of files, globs or `journal:` units. |
| `--cfg PATH` | Config file; `--cfg none` loads none. |
| `--auth-key-path`, `--key` (deprecated alias), `--no-auth-key`, `--agentKeyIndex N` (0-based; -1 = all agent keys) | SSH key and fast reconnect; see [auth-and-config.md](auth-and-config.md). |
| `--known-hosts-path`, `--trustAllHosts`, `--trustChangedHostKeys` | Host key file and trust decisions. |
| `--plain` | Payload only, without the `REMOTE` source prefix; implies quiet and no color. |
| `--quiet` | No periodic `STATS` lines (and so no progress fields in them). |
| `--progress` | `dcat`, `dgrep`, `dmap`: write the read progress as `PROGRESS` lines to stderr, also with `--plain`/`--quiet`, and a final line at the end (see [output-and-status.md](output-and-status.md#read-progress)). |
| `--noColor` | No terminal colors (payload, tables, interactive output). |
| `--logger`, `--logDir`, `--logLevel` (info; serverless warn), `--log-payload` | Client log sink; see [output-and-status.md](output-and-status.md). |
| `--interactive-query`, `--control-tty` (/dev/tty), `--edit-mode emacs\|vi` | Interactive control; see [interactive-control.md](interactive-control.md). |
| `--control json`, `--control-in PATH` (stdin), `--control-out PATH` (stderr) | Interactive control for programs and agents, JSON requests and responses; logs to the file only (logger `fdata`) unless `--logger` is given; see [agent-control.md](agent-control.md). |
| `--hostname-override` | Hostname shown in logs/output (env `DTAIL_HOSTNAME_OVERRIDE`). |
| `--version`, `--pprof ADDR`, `--cpuprofile`, `--memprofile`, `--profile`, `--profiledir` (profiles) | Version and profiling. |

## Filter semantics (dgrep, dtail)

- Go `regexp` syntax. `--grep` wins over `--regex` when non-empty.
- The default `.` matches non-empty lines; `.*` matches every line,
  empty ones included. `dgrep --regex ''` is invalid.
- `--invert` prints non-matching lines (with `.` only empty lines remain).
- `--before`/`--after` add context lines; `--max N` stops a file's read
  after N matching lines (per file and server, not fleet-wide).

## Time bounds

- `--timeout N` (`dtail`, `dmap`; default 0 = none): seconds a server
  collects data before it disconnects; for `dtail` that also ends the run.
- `--shutdownAfter N` (`dtail`): ends the whole client run after N seconds.
  The way for an agent to bound a follow.

## Query-driven dtail

`dtail --query` aggregates followed lines with the MapReduce language; the
query's `from` table chooses the input, so `--regex`/`--grep` are ignored
and `--invert` too (`WARN: Ignoring --invert: ...`).

## Positional arguments

`dmap` and `dtail` take a positional query only when it contains `select `;
every other positional argument (all of them for `dcat`/`dgrep`) is a file. An interactive `dtail` keeps `--before`, `--after`
and `--max` for following lines and leaves them out of the query.

## Interactive defaults

- `dtail` is interactive by default when stdin and stdout are a terminal
  and the control TTY opens; `--no-interactive` turns it off and wins over
  `--interactive-query`. Pipes, cron and scripts are not interactive by
  default.
- `dmap` without a query opens the query builder on a terminal; with
  `--no-interactive` (or no terminal) it exits `1` instead.
- `dcat`/`dgrep` are interactive only with `--interactive-query`.
- Scheduled and continuous jobs are never interactive.
