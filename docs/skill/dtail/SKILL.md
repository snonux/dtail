---
name: dtail
description: "Run, explain and administer DTail, the distributed tail/cat/grep/MapReduce log tool over SSH (dcat, dgrep, dtail, dmap, dtailhealth, dserver). Use when reading, searching, following or aggregating logs on many hosts or locally, writing DTail queries, or setting up dserver. Triggers on: dtail, dcat, dgrep, dmap, dserver, distributed log grep."
---

# DTail

DTail reads, searches, follows and aggregates log files (plain, gzip, zstd)
and systemd journals on many hosts at once. Clients connect over SSH (port
`2222`) to a `dserver` on every host, or run **serverless** on local files
when no server is named. This skill covers the clients, the MapReduce query
language, the interactive mode, `dserver` setup and configuration, its
scheduled jobs, and building/testing DTail itself.

## Choose the command

| Need | Command |
| --- | --- |
| Print whole files or a journal snapshot | `dcat` |
| Search existing files with a Go regex (context, invert, max count) | `dgrep` |
| Follow new lines, optionally filtered | `dtail` |
| Aggregate existing logs or CSV with a SQL-like query | `dmap --query '...'` |
| Aggregate new lines continuously | `dtail --query '...'` |
| Build a query interactively (a person at a terminal) | `dmap` without `--query` (query builder) |
| Change filter/query/files of a running session | interactive control (`:reload`, `:query`, `:tail`) |
| Check a dserver endpoint (Nagios style) | `dtailhealth --server HOST:PORT` |
| Serve reads, run scheduled MapReduce jobs | `dserver` |

## Rules for agents

1. **Name the servers.** No `--servers` means local serverless mode, not
   "all hosts". Get the host list (comma list or a file of `HOST[:PORT]`
   lines) from the user or their context.
2. **Prefer finite commands.** `dgrep`, `dcat`, `dmap` end on their own.
   `dtail` follows forever: bound it with `--shutdownAfter SECONDS`.
3. **Never leave a client waiting for a terminal.** Pass `--no-interactive`
   to `dtail` and `dmap` when a TTY may be attached; always give `dmap` a
   `--query` (without one it opens the query builder on a terminal, or exits
   `1` with `no mapreduce query specified`).
4. **Quote** globs, regexes and queries in single quotes (queries hold `$`).
   Use `--files`, `--regex`/`--grep` and `--query` rather than positional
   arguments; flags go before positional paths.
5. **Machine-readable output:** `--plain --logger stdout`, or at least
   `--noColor`: colors are not turned off for pipes. Server warnings can
   still appear in the stream.
6. **Check the exit status and WARN lines** before calling a result
   complete: `dgrep` exits `0` with no match; `dmap` exits `0` even with an
   incomplete result and says so in a WARN line.
7. **Report scope:** hosts and paths queried, what failed or was missing.
   A subset of hosts answering is not a fleet-wide answer.
8. **Host keys:** an unknown host asks a question on the terminal. Use
   `--trustAllHosts` only if the user accepts that trust decision; a
   *changed* key is refused unless `--trustChangedHostKeys` is also given.
9. **Show the progress of every `dcat`, `dgrep` and `dmap` periodically.**
   Always pass `--progress` and run the command in the background, stdout
   to a file and stderr to another: `dgrep --plain --progress ... >
   out.txt 2> progress.txt &`. About **every 10 seconds** until it ends,
   read the last `PROGRESS` line (`tail -n 1 progress.txt`) and show the
   user one line: the `bar` field (a ready-made progress bar), percent,
   amount and ETA, e.g. "`[######--------------]` 30%, 1.2 of 4.0 GiB,
   about 34 s left, 2/2 servers reporting". Do not wait silently for the
   result. When the command ends, report from the final line, then the
   result. Fields and caveats: [output-and-status.md](references/output-and-status.md#read-progress).

## Quick reference

```sh
dcat  --servers 'web01,web02:2223' --files '/var/log/app/*.log'
dgrep --servers servers.txt --files '/var/log/app/*.log' --regex 'ERROR|FATAL' --before 2 --after 3
dtail --servers servers.txt --files 'journal:myapp.service' --regex ERROR --shutdownAfter 60 --no-interactive
dmap  --servers servers.txt --files '/var/run/dserver/log/*.log' --no-interactive \
      --query 'from STATS select $hostname,max($goroutines) group by $hostname'
dmap  --files data.csv --query 'select name,avg(age) group by name logformat csv'   # serverless
dgrep --servers servers.txt --files '/var/log/app/*.log.gz' --regex ERROR \
      --plain --progress > out.txt 2> progress.txt &                             # watch progress.txt
printf 'a\nb\n' | dgrep --regex a --plain --logger stdout                           # stdin
dtailhealth --server web01:2222
```

## Reference files

Load only the file the task needs.

**Using the clients**
- [servers-and-sources.md](references/servers-and-sources.md): server lists,
  discovery, serverless mode and stdin, `--files` lists and globs, paths with
  spaces, `journal:` units, compressed files.
- [client-flags.md](references/client-flags.md): every flag of `dcat`,
  `dgrep`, `dtail`, `dmap`, `dtailhealth` with defaults; regex semantics,
  `--timeout`, `--max`, `--shutdownAfter`.
- [auth-and-config.md](references/auth-and-config.md): client config file,
  SSH keys/agent, `known_hosts` and trust flags, auth-key fast reconnect,
  connect and handshake timeouts, environment variables.
- [output-and-status.md](references/output-and-status.md): output line
  format, `--plain`, read progress (`--progress`, ETA), client logging
  (`fout`, `--log-payload`), exit statuses, protocol-5 compatibility.
- [troubleshooting.md](references/troubleshooting.md): symptom -> cause ->
  fix for common failures (empty groups, hanging client, refused hosts,
  `journal-v1`, exit `2`).

**MapReduce**
- [query-language.md](references/query-language.md): grammar, clauses,
  aggregations, where operators, `set` functions, tables, fields vs
  `$variables`, escaping.
- [mapreduce-results.md](references/mapreduce-results.md): `interval`
  reporting, `outfile`/`outfile append`, `dtail --query`, CSV input/output,
  incomplete results.
- [log-formats.md](references/log-formats.md): `default`, `generic`,
  `generickv`, `csv` and stub formats, their variables, the default line
  layout, writing a custom format and probe.

**Interactive mode (a human at a terminal)**
- [interactive-control.md](references/interactive-control.md): when it is
  on, control commands, `:reload` flags and quoting, `:query`/`:tail`
  switching, pausing output, session semantics.
- [interactive-editing.md](references/interactive-editing.md): line editor,
  guided Tab completion of queries and remote paths, field discovery
  (`:fields`, `:probe`), vi mode, `$EDITOR`, colors.
- [query-builder.md](references/query-builder.md): `dmap` without a query on
  a terminal.

**Running dserver**
- [dserver-setup.md](references/dserver-setup.md): build variants, install,
  systemd units, SSH keys for users, firewall, monitoring, `dserver` flags.
- [dserver-config.md](references/dserver-config.md): config file, read
  permissions (allow/deny rules), limits, output buffers, auth keys,
  journal, shared reads, path completion.
- [dserver-jobs.md](references/dserver-jobs.md): scheduled and continuous
  MapReduce jobs, date placeholders, failure and retry semantics.

**Developing DTail**
- [development.md](references/development.md): `make` targets, unit and
  integration tests, benchmarks, profiling, PGO, `dtail-tools`.

## Sources of truth

The user documentation lives in `doc/` at the repository root
(`quickstart.md`, `examples.md`, `querylanguage.md`, `logformats.md`,
`interactive-query.md`, `server-configuration.md`, `installation.md`,
`auth-key-fast-reconnect.md`, `testing.md`); developer notes in `docs/dev/`
and `AGENTS.md`. Clients and `dserver` speak protocol 5 and must be upgraded
together. When a deployed binary may be older than this skill, check
`--version` and `-h` before relying on a flag.
