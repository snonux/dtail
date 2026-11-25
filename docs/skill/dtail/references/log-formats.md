# Log formats

A log format is the parser that turns a line into query fields. Select it
with the query clause `logformat NAME` (exact, lower case); otherwise a
query with a `from` clause uses `default` (or the server's
`Server.MapreduceLogFormat`) and a query without `from` uses `generic`.

## Built-in formats

| Name | Reads | Fields |
| --- | --- | --- |
| `default` | DTail's own structured lines (below) | barewords from `key=value` fields + all `$variables` |
| `generic` | any line | common `$variables` only |
| `generickv` | `key1=value1\|key2=value2\|...` | barewords + common `$variables`; malformed fields skipped |
| `csv` | header line per file, then rows (may be shorter than the header, not longer) | one bareword per column |
| `mimecast`, `mimecastgeneric` | proprietary builds only (`DTAIL_USE_PROPRIETARY=yes` with the non-public `*_proprietary.go` files) | stubs error out otherwise |
| `custom1`, `custom2` | templates for your own parser | "not implemented" until registered |

A format that cannot be created makes dserver log an error and fall back
to `generic`.

## The default line layout

```text
INFO|1002-071143|1|stats.go:56|8|13|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1
```

Fields: severity, time, pid, caller, cpus, goroutines, cgocalls, loadavg,
uptime, `MAPREDUCE:<TABLE>`, then `key=value` barewords. Only lines whose
first field starts with `INFO` are parsed. A time of `YYYYMMDD-HHMMSS`
fills `$date`, `$hour`, `$minute`, `$second`; the short `MMDD-HHMMSS` that
dserver's own log uses fills no `$date`. Make an application log this way
to query it with `from TABLE`; dserver's own log carries `MAPREDUCE:STATS`
lines (`from STATS`).

## Variables

- Common (every format): `$hostname`, `$server` (alias), `$line`,
  `$empty`, `$timezone`, `$timeoffset` (seconds east of UTC).
- `default` only: `$time` (raw timestamp), `$date`, `$hour`, `$minute`,
  `$second`, `$severity`/`$loglevel`, `$caller`, `$pid`, `$cpus`,
  `$goroutines`, `$cgocalls`, `$loadavg`, `$uptime`.
- Using a `$variable` the format cannot fill prints a plan-time warning
  (`warning: $service is not a known variable for log format "default";
  did you mean bareword service?`).

## Field discovery

Interactive sessions probe a sample of the files (last 256 KiB / 500 lines
per file, first 500 lines of compressed files, `journalctl -n 500`) and list
tables, fields, `$variables` and sample values with `:fields`; Tab
completion uses the same catalog. The `csv` probe answers only for files
that look like CSV. A sample is not a schema: fields seen there may be
missing elsewhere. See [interactive-editing.md](interactive-editing.md).

## Writing a custom format (Go, recompile DTail)

1. Copy a template: `cp internal/mapr/logformat/generic.go
   internal/mapr/logformat/foo.go`, rename to `fooParser`.
2. Keep `defaultParser` in a **named** field (`base defaultParser`), not
   embedded, so you must write your own `MakeFieldsInto`.
3. Implement `MakeFields(maprLine, sourceID string) (map[string]string,
   error)` and the allocation-free `MakeFieldsInto(dst, maprLine, sourceID)
   error` (clear `dst` first). Fill fields with
   `p.base.addDefaultFields`, `p.base.addKeyValueField`/`addDynamicField`
   and `protocol.ScanField`; return `ErrIgnoreFields` for lines to skip.
   Stateful parsers key state by `sourceID` and implement
   `SourceReleaser`.
4. Register: `func init() { mustRegisterParser("foo",
   wrapParserFactory(newFooParser)) }`, or from outside the package
   `logformat.RegisterParser("custom1", factory)`.
5. Optional probe support: implement `Prober` (`Probe(ProbeSample)
   (ProbeResult, error)`, often wrapping `ProbeWithParser`) and
   `ProbeTabler` (`ProbeLineTable(line) (table, ok)`) when the format has
   tables. List only upper-case table names a query can select.
6. `make build`, then query with `... logformat foo`.

Full guide: `doc/logformats.md`; `internal/mapr/logformat/custom1.go` holds
a commented probe example.
