# MapReduce results

## Where the work happens

- Server mode: every `dserver` parses and aggregates its own lines and
  sends partial aggregates; the client merges them per group.
- Serverless mode: the client aggregates locally.
- `dmap` reads existing data to its end, then the servers send their rest
  and the session closes. `dtail --query` aggregates followed lines until
  `--shutdownAfter`, Ctrl-C or `:tail`.

## Reporting and `interval`

- Each server sends what it aggregated once per `interval` (default 5 s,
  `0` = 1 s), and the rest when its input ends.
- The client prints the merged table: first about 1 s after every
  connected server's first aggregates arrived (they come one interval
  after the start), at most half an interval after the first server's;
  then once per interval; and a final table when the query ends.
  Unchanged or empty tables are not printed again.
- Cumulative (`dmap`, `dtail --query` with an outfile, scheduled jobs):
  every report holds everything so far. Non-cumulative (`dtail --query`
  without an outfile, continuous jobs): each report shows only what arrived
  since the previous one.
- Client and servers run on their own clocks; a report can lack up to one
  interval of the latest lines.
- On a terminal the raw query is printed (highlighted) above each table;
  piped output gets it as plain contiguous text.

## Outfiles

- `outfile PATH` writes a CSV result (header + rows, via `PATH.tmp` and a
  rename) and `PATH.query` with the query, **instead of** printing the table
  to stdout: read the file. Without `append`, every interim report replaces the file, so
  it always shows the result so far.
- `outfile append PATH` appends; the header only when the file is new or
  empty. `dmap`/`dtail --query` append the **final** result exactly once
  (interim reports only check the file can be opened). Until then the
  result is only in memory: SIGKILL or a crash loses all of it. Continuous
  jobs append each interval's rows once; scheduled jobs their final result
  once per run.
- `outfile ./append:x.csv` writes a file literally named `append:x.csv`;
  `outfile append:x.csv` is rejected.
- Outfiles are CSV with standard quoting and can be read back with
  `logformat csv`. A value with CR/LF fails the export.

## CSV input

`logformat csv` reads a header on the first line of each file (fields named
after the columns), standard double-quote quoting, one record per physical
line (embedded newlines unsupported):

```sh
dmap --files data.csv --query 'select name,avg(age) where age > 40 group by name logformat csv outfile result.csv'
```

## Completeness

- `dmap` exits `0` and writes its outfile even when a server reported
  `map: incomplete result` or a session ended without the close handshake;
  it logs `The mapreduce result may be incomplete|<server>: <reason>` at
  WARN. Missing or denied files show up as server warnings instead. Check
  both before trusting the result.
- A large final result is not cut by a deadline: a slow client only slows
  the server down; only session teardown or the idle timeout ends it early.
- `limit N` limits result rows, not the data read. Without `limit` the
  printed table (stdout) shows at most 10 rows; outfiles get every row.
- A query replaced by `:reload`/`:query`/`:tail` reports once more; up to
  one interval can be missing from that last table.
- Scheduled jobs write nothing while any failure occurred within their
  TimeRange; see [dserver-jobs.md](dserver-jobs.md).
