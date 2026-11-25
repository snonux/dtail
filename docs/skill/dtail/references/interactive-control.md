# Interactive control

Interactive mode lets a person change a running session from the terminal.
Agents normally turn it off (`--no-interactive` for `dtail`/`dmap`; never
pass that flag to `dcat`/`dgrep`, which reject it).

## When it is on

- `dtail`: by default when stdin and stdout are a terminal and the control
  TTY (`--control-tty`, default `/dev/tty`) opens.
- `dcat`, `dgrep`, `dmap` with a query: only with `--interactive-query`.
- `dmap` without a query on a terminal: the query builder
  ([query-builder.md](query-builder.md)).
- Not by default in pipes, redirections, cron or CI (an explicit
  `--interactive-query` still enables it if the control TTY opens); never
  in scheduled/continuous jobs.

## Commands

| Command | Effect |
| --- | --- |
| `:reload FLAGS` | Change filter, files or query on the running sessions. |
| `:query QUERY` | `dtail`: switch from following lines to the query (or replace it); `dmap`: replace the query. `:query` alone shows it. |
| `:tail [REGEX]` | `dtail`: back to following lines with the previous (or given) regex. |
| `:show` | Current settings: `workload=`, mode, files, `invert=`, `servers=`, `sessions=`. |
| `:fields [TABLE]` | Tables, fields, `$variables`, sample values of the files (probe). |
| `:probe` | Probe now; report which servers answered (waits up to 5 s). |
| `:help` | Help with per-mode examples. |
| `:quit` | End (Ctrl-D on an empty line too). |
| empty line | Pause the output; the next empty line resumes. |

`:query`/`:tail` take the rest of the line unquoted. The query must parse
and the regex compile before anything changes.

## `:reload` flags

- All modes: `--files`, `--plain`, `--quiet`, `--timeout`.
- Grep modes (`dgrep`, `dtail` following lines): `--grep`/`--regex`,
  `--invert`, `--before`, `--after`, `--max`.
- Query modes (`dmap`, `dtail` running a query): `--query`.
- Omitted flags keep their values; at least one flag is required; files and
  a query must stay non-blank. `--invert` carries over; `--invert=false`
  resets it. `--regex ''` matches every line; `--grep ''` is rejected.
- Quoting is POSIX-shell-like without expansion: single quotes are
  literal; inside double quotes a backslash escapes only `\`, `"`, `$` and
  a backtick, and is kept before anything else.

```text
:reload --regex 'ERROR|WARN' --after 2
:reload --files '/var/log/app/*.log,journal:app.service'
:query from STATS select $hostname,max($goroutines) group by $hostname
:tail timeout
```

## Pausing

Typing a command pauses the output until Enter (a hint line says so).
`:help` and `:fields` leave it paused until the next empty line.
`:reload`, `:query`, `:tail`, `:probe` resume first. A rejected line keeps
the pause. Paused output applies backpressure to the servers (nothing is
buffered, except up to 4 MiB held while Tab waits for a server). A line
started while paused gets the prompt `dtail> `.

## Session semantics

- Every server must speak protocol 5; the session starts with `SESSION
  START`, a rejected start fails that connection.
- A reload updates every active session, all or nothing (a failure rolls
  back the updated ones, the old workload keeps running). Servers down or
  without a session are skipped and named; a connecting one starts with
  the new workload. A reload to `journal:` checks `journal-v1` on all
  active servers first.
- Each update waits at most 10 s; a failing reload can take ~20 s per
  stalled server.
- Successful reloads reuse the SSH sessions and drop late output of the
  replaced workload.
- `dtail` switching to a query sets the regex, `--invert`, context and
  `--max` aside and restores them on `:tail`. The last query table is
  reported once more on the switch back.

Full rules: `doc/interactive-query.md`.
