# Interactive line editing, completion and field discovery

Applies when the control TTY is a terminal; a non-terminal control TTY
reads plain lines.

## Keys

- Emacs-style editing by default; Up/Down recall history; Ctrl-C keeps its
  meaning (statistics, then exit); lines are at most 4095 characters.
- `--edit-mode vi` (or `Client.EditMode: "vi"`): every line starts in
  insert mode, Esc enters normal mode (motions with counts, `d`/`c`/`y`,
  `x X D C s S Y`, `i a I A`, `r ~ p P`, `u`/Ctrl-R, `k`/`j` history). The
  cursor shape shows the mode.
- Alt-E or Alt-V opens the line in `$VISUAL`/`$EDITOR`/`vi`; the saved text
  (line breaks as blanks) comes back to be entered.
- Bracketed paste: pasted text is inserted as typed text; a pasted line
  ending in a line break runs.

## Tab completion

- Commands, the mode's `:reload` flags, and the current
  `--grep`/`--regex`/`--query`/`--files` value.
- **Queries** (after `:query ` or inside `--query '...'`): guided, one step
  per Tab: `from` -> table -> `select` -> items -> `where` -> `group by` ->
  `order by` -> `limit`. One possible next step is inserted at once;
  several are listed under a heading (`select item`, `field to count`,
  `value for x`) in groups (`aggregation:`, `field:`, `variable:`,
  `next:`), cut after three rows with `... (N more, type to narrow)`.
  Clauses already present are not offered again; rare clauses (`set`,
  `interval`, `outfile`, `logformat`, `rorder`) only when typed. Once a
  table is named, fields, `$variables` and values come from that table's
  lines only. Formats without tables start at `select`.
- **Paths** (inside `--files`): the last comma-separated entry from a
  remote directory listing of the first server up. Only files the user's
  `readfiles` rules allow; directories by an approximation of the rules;
  hidden names after a typed `.`; `journal:` completes unit names. Listings
  are cached 2 minutes and prefetched for the session's directories.
  `Server.PathCompletionDisable: true` turns listings off on a server.
- A Tab that needs a server (a table not probed within a minute, or an
  uncached directory) waits up to 1 s; output arriving meanwhile is held
  (up to 4 MiB) and printed after the line, never over it.

## Field discovery

- An interactive session probes up to 3 servers in the background: at
  start, after each applied `:reload`, every 60 s while output runs.
  Each server samples up to 8 files (last 256 KiB / 500 lines; compressed
  files' first 500 lines; `journalctl -n 500`).
- `:fields` prints the catalog per log format: tables, fields with sample
  values, `$variables`, answering servers, age, errors.
- `:fields TABLE` (case-insensitive) first runs a **table probe**: servers
  scan up to 4 MiB of each file for that table's lines only, so rare tables
  get fields too (waits up to 2 s, repeated at most once a minute).
- `:probe` requests a round now and reports answered/busy/failed servers.
- A probe is a sample: fields seen may be missing elsewhere. Probes never
  change exit statuses; non-interactive clients never probe.
- Probe sampling is bounded (10 s timeout, 48 KiB answer, one probe per
  session at a time) and checked against the same read permissions.

## Colors

With terminal colors on, control output, `:help`, `:show`, `:fields`, the
prompt and candidate lists are styled, and queries are highlighted (`:show`,
the `query: ...` echo after a reload, above result tables). Styles:
`Client.TermColors.Interactive`. Off with `--noColor`, `--plain` or
`Client.TermColorsEnable: false`.
