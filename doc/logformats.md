Log Formats
===========

A log format tells DTail how to extract fields from a log line, for use in
the [DTail Query Language](./querylanguage.md). Either log in a format DTail
knows, or implement your own (below).
`integrationtests/mapr_testdata.log` is an example in the `default` format.

## Available log formats

| Name | Lines | Fields |
|------|-------|--------|
| `default` | DTail's own `MAPREDUCE` lines. | Default and common variables, `key=value` fields. |
| `generic` | Any line. | Common variables only. |
| `generickv` | `key1=value1\|key2=value2\|...` | Common variables, `key=value` fields. |
| `csv` | Comma separated, header on the first line of each file. | Common variables, one field per column. |
| `mimecast`, `mimecastgeneric` | Mimecast formats. | Only in builds with the `proprietary` tag. |
| `custom1`, `custom2` | Yours. | Templates to register your own parser under. |

**default.** The line layout, `|`-separated:

```
INFO|TIME|PID|CALLER|CPUS|GOROUTINES|CGOCALLS|LOADAVG|UPTIME|MAPREDUCE:TABLE|key1=value1|key2=value2|...
INFO|1002-071143|1|stats.go:56|8|13|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1
```

* Lines not starting with `INFO`, without `MAPREDUCE:` at that position or
  with fewer than 11 fields are ignored.
* A field after the table without `=` is a parse error.

**generickv.** Fields without `=` are skipped; every line is accepted.

**csv.**

* Values are taken as they are, blanks around them included.
* A row may have fewer columns than the header; more is a parse error.
* Fields and header names with commas or double quotes use standard CSV
  quoting (`"a ""b"", c"`).
* A record must fit on one line: embedded CR/LF is unsupported, and
  exporting such a value fails instead of writing corrupt rows.
* Query outfiles use the same encoding and can be read with
  `logformat csv`.

**mimecast, custom.** Without the `proprietary` build tag
(`DTAIL_USE_PROPRIETARY=yes make build`, sources not in this repository)
the `mimecast` parsers are stubs returning the error `the mimecast
logformat is not available in this build of DTail`; `custom1` and `custom2`
return "not implemented" until you register a parser.

### Selecting a log format

| Query | Log format |
|-------|------------|
| With a `logformat NAME` clause | `NAME` (case sensitive). |
| With `from` (also `from .` and `from *`) | `default`, or the server's `MapreduceLogFormat`. |
| Without `from` | `generic`. |

```shell
% dmap --files /var/log/example.log --query 'select service,sum(bytes) group by service logformat generickv'
```

* `MapreduceLogFormat` is set in the Server section of `dtail.json`; see
  [server configuration](./server-configuration.md).
* A format whose parser cannot be created (unknown name, a stub or
  template) makes dserver log an error and fall back to `generic`.
* The client does not know a server's `MapreduceLogFormat`: its warning
  about unknown `$`-variables assumes `default`.

## Log format variables

Fields starting with `$` are variables set by DTail; all other fields are
barewords extracted from the line.

### Common variables:

Available in every built-in format:

| Variable | Value |
|----------|-------|
| `$line` | The whole log line. |
| `$hostname`, `$server` | The server's hostname. |
| `$timezone` | The server's time zone. |
| `$timeoffset` | Offset of `$timezone` in seconds east of UTC. |
| `$empty` | The empty string. |
| `*` | Placeholder, e.g. `count(*)` to group by everything. |

### Default log format variables:

Only in the `default` format, from the positional fields of the line:

| Variable | Value |
|----------|-------|
| `$time` | The timestamp as it appears in the line. |
| `$date` | `YYYYMMDD`; only for the timestamp form with a year. |
| `$hour`, `$minute`, `$second` | `HH`, `MM`, `SS`. |
| `$severity`, `$loglevel` | The log level. Always `INFO`, as other lines are ignored. |
| `$pid` | Process ID. |
| `$caller` | Caller of the logger. |
| `$cpus` | Number of CPUs. |
| `$goroutines` | Number of goroutines. |
| `$cgocalls` | Number of cgo calls. |
| `$loadavg` | 1 minute load average. |
| `$uptime` | Uptime. |

Two timestamp forms are accepted: `YYYYMMDD-HHMMSS` (e.g.
`20211002-071209`) and the `MMDD-HHMMSS` form dserver stamps on its own
lines (e.g. `1002-071143`), which has no year and so sets no `$date`.

## Implementing your own log format `Foo`

1. Copy a template into the `logformat` package:

   ```shell
   % cp internal/mapr/logformat/generickv.go internal/mapr/logformat/foo.go
   ```

2. Rename `genericKV` to `foo` throughout (`fooParser`, `newFooParser`)
   and write your parsing in `MakeFieldsInto`:

   ```go
   type fooParser struct {
   	base defaultParser // a named field, never embedded
   }

   func (p *fooParser) MakeFieldsInto(dst map[string]string, maprLine, _ string) error {
   	clear(dst)
   	p.base.addDefaultFields(dst, maprLine) // the common $variables
   	start := 0
   	for {
   		token, next, done := protocol.ScanField(maprLine, start) // next |-field
   		_ = p.base.addKeyValueField(dst, token) // YOUR CODE: fill the fields
   		if done {
   			break
   		}
   		start = next
   	}
   	return nil
   }
   ```

3. Register it in `foo.go`:

   ```go
   func init() {
   	mustRegisterParser("foo", wrapParserFactory(newFooParser))
   }
   ```

4. Recompile. Queries can now say `... logformat foo`.

Rules:

* Fill fields with `p.base.addDefaultFields`, `p.base.addKeyValueField`
  (a `key=value` token) or `p.base.addDynamicField` (key and value), not by
  writing map keys by hand: they fill only what the query uses.
* Return `ErrIgnoreFields` for a line your format ignores.
* Keep `defaultParser` in a named field. Embedded, its `MakeFieldsInto`
  would silently parse every line in DTail's own layout.
* `sourceID` identifies the file a line came from; a parser with per-file
  state (like csv headers) keys it by that.
* From outside the package use `logformat.RegisterParser("custom1",
  factory)`, the intended way to fill `custom1` and `custom2`.

The parser interfaces, the full template and the registration from other
packages are described in
[log format parsers and probes](../docs/dev/logformat_probes.md).

## Custom probes

DTail samples log lines to find the tables, fields and `$`-variables a
format extracts, for the Tab completion and `:fields` of
[interactive queries](./interactive-query.md).

A format needs no probe of its own: the generic probe parses the sample
with your parser, so a newly registered format is discoverable at once.
Implement the optional `Prober` interface to name tables, list a fixed
schema, hide fields or supply better sample values, and `ProbeTabler` when
your format has tables. List only upper-case table names, as a query's
table name is upper-cased.

`internal/mapr/logformat/custom1.go` holds a commented example. Types,
rules and limits:
[log format parsers and probes](../docs/dev/logformat_probes.md).
