# dmap query builder

`dmap` without a query (no `--query`, no positional argument holding
`select `) opens a query builder instead of failing, when stdin and stdout
are a terminal and the control TTY opens, or with an explicit
`--interactive-query`. `--no-interactive` never opens it; without a
terminal the missing query stays an error (`no mapreduce query specified`,
exit `1`). **Agents always pass `--query`.**

## How it works

1. **Builder.** Holds idle sessions on at most 3 of the servers (no read,
   no output), probes the `--files` on them for tables and fields, and
   shows the prompt `query> `. Sessions are kept alive with SSH keepalives
   every 60 s; a lost one is replaced after a backoff (2 s doubling to
   60 s) by the next server in rotation.
2. **Run.** Enter on a query that parses closes the idle sessions and runs
   the query on new connections to **all** servers exactly as
   `dmap --query` would (result table, outfile, exit status), then `dmap`
   exits. With `--interactive-query` it continues as interactive `dmap`
   (`:query` replaces the query; Up recalls the built query).

## Builder input

- The typed line is the query (unquoted); `:query QUERY` works too.
- Tab completes the query, guided from `from` (see
  [interactive-editing.md](interactive-editing.md)); Tab on an empty line
  starts with `from `.
- Commands: `:fields [table]`, `:probe`, `:show` (`workload=builder`, the
  files), `:help`, `:reload --files PATH` (its only flag; just changes the
  files and re-probes), `:quit` (nothing runs; exit `0`, or `1` if a held
  connection failed). `:tail` is rejected.
- A command word without its colon (`quit`, `exit`, `q`, `help`, `show`,
  `fields`, `probe`, `reload`, `query`) gets a hint instead of a parse
  error. An empty line answers with what to type.
- A query that does not parse, or one entered while no files are named, is
  answered and the builder stays open.
- A multi-line paste becomes one line that runs only on Enter; input still
  pending when the builder ends is discarded, not passed to the shell.
- Log lines (connection errors, server warnings) are printed above the
  prompt.

## Limits

- The run needs connection slots again; a dserver at `MaxConnections` that
  has not noticed the builder's close yet may refuse it (exit `1`).
- Host key answers are not carried over from builder to run.
- A positional argument without `select ` is taken as a file, so a
  mistyped query opens the builder (it hints when the files look like a
  query).

Full description: "The `dmap` query builder" in `doc/interactive-query.md`.
