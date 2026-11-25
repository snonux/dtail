Log Format Parsers and Probes
=============================

Developer detail of custom log formats: the parser interfaces, a full
parser template, registration, and the probes that describe a format's
fields to the interactive query completion. The user guide is
[log formats](../../doc/logformats.md); the probe command and wire format
are in [probe.md](./probe.md).

All code is in `internal/mapr/logformat/`.

## Parser interfaces

`generickv.go` is the smallest complete parser and a good one to read
first.

* `Parser` declares `MakeFields(maprLine, sourceID string)
  (map[string]string, error)`: allocate a field map and parse the line into
  it.
* `FieldsIntoParser` is the optional, allocation-free form
  `MakeFieldsInto(dst map[string]string, maprLine, sourceID string) error`:
  fill a map owned by the caller (clear it first). The MapReduce aggregator
  prefers it and falls back to `MakeFields`.
* `SourceReleaser` (`ReleaseSource(sourceID string)`) is for parsers with
  per-source state, like the csv header rows. The aggregator calls it once
  it parsed the last line of a source.
* `WhitespacePreservingParser` (`PreserveWhitespace() bool` returning true)
  is for parsers that need the line as it is, only its LF or CRLF removed,
  as `csv` does. Other parsers get every line with the blanks around it
  trimmed.

The arguments:

* `maprLine` is the whole raw log line.
* `sourceID` is the stable identifier of the file or stream the line came
  from. Stateful parsers key their per-file state by it; stateless parsers
  ignore it.
* `protocol.ScanField` scans the next `|`-delimited field of the line. The
  delimiter is a detail of the protocol package (`|` for log fields,
  `protocol.CSVDelimiter` `,` for CSV fields).

Field names starting with `$` are variables; all others are bareword fields
extracted from the line.

Only parsers in the `logformat` package learn the query (the unexported
`setQuery`), so that they fill only the fields the query uses. A parser
registered from outside the package is built without it and fills all its
fields.

## Full parser template

Copy `generic.go` or `generickv.go` to `foo.go` and rename:

```go
package logformat

import (
	"errors"

	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/protocol"
)

type fooParser struct {
	// Keep the defaultParser in a named field, NOT as an embedded field:
	// embedding it would promote defaultParser.MakeFieldsInto onto
	// fooParser, so the allocation-free path would silently parse every
	// line in DTail's own MAPREDUCE layout instead of yours (see the
	// FieldsIntoParser doc comment in parser.go). With a named field the
	// compiler insists on a MakeFieldsInto of your own.
	base defaultParser
}

func newFooParser(hostname, timeZoneName string, timeZoneOffset int) (*fooParser, error) {
	defaultParser, err := newDefaultParser(hostname, timeZoneName, timeZoneOffset)
	if err != nil {
		return &fooParser{}, err
	}
	return &fooParser{base: *defaultParser}, nil
}

func (p *fooParser) setQuery(query *mapr.Query) {
	// Lets the base parser populate only the fields the query needs.
	p.base.setQuery(query)
}

func (p *fooParser) MakeFields(maprLine, sourceID string) (map[string]string, error) {
	fields := make(map[string]string, p.base.fieldsCapacity)
	if err := p.MakeFieldsInto(fields, maprLine, sourceID); err != nil {
		if errors.Is(err, ErrIgnoreFields) {
			return nil, err
		}
		return fields, err
	}
	return fields, nil
}

func (p *fooParser) MakeFieldsInto(dst map[string]string, maprLine, _ string) error {
	clear(dst)
	p.base.addDefaultFields(dst, maprLine)
	start := 0

	for {
		token, next, done := protocol.ScanField(maprLine, start)
		..
		<YOUR CUSTOM CODE HERE>
		..
		if done {
			break
		}
		start = next
	}

	return nil
}
```

Populate fields through `p.base.addDefaultFields(...)` (the common
`$`-variables), `p.base.addDynamicField(...)`/`p.base.addKeyValueField(...)`
(bareword fields, honoring the query's field plan) and
`protocol.ScanField`, not by writing raw keys into the map by hand. If your
parser ignores a line, return `ErrIgnoreFields`.

## Registration

There is no switch statement to extend: `NewParser` looks parsers up in a
registry of parser factories (`parser.go`). The built-in formats are
registered by `registerBuiltInParsers`. A file in the package registers its
format from an `init` function:

```go
func init() {
	mustRegisterParser("foo", wrapParserFactory(newFooParser))
}
```

`logformat.RegisterParser("name", factory)` is exported, so code outside
the package can register or replace a parser. That is the intended way to
implement `custom1` and `custom2`:

```go
func init() {
	logformat.RegisterParser("custom1", func(hostname, timeZoneName string, timeZoneOffset int) (logformat.Parser, error) {
		return newMyCustom1Parser(hostname, timeZoneName, timeZoneOffset)
	})
}
```

The factory must return a new parser on every call (see the probe rules
below).

## Custom probes

DTail can probe a sample of log lines to find out which tables, fields and
`$`-variables a log format extracts from them, e.g. to complete MapReduce
queries. A format needs no probe of its own:

* `logformat.Probe(name, hostname, sample)` builds the format's parser with
  no query (so it fills all fields) and runs the generic probe.
* The generic probe, `logformat.ProbeWithParser(parser, sample)`, parses
  every sampled line with your `MakeFields`/`MakeFieldsInto`, skips lines
  it ignores (`ErrIgnoreFields`) or fails on, and collects the field names
  and a few sample values.
* `logformat.Names()` lists every registered format and
  `logformat.KnownVariables(name)` the `$`-variables a built-in format can
  populate.

### Types (`probe.go`)

**`ProbeSample`** holds the sampled lines (`ProbeLine`: `Line` and
`SourceID`), the lines of each source in the order of the source. A
stateful format reads it as its parser reads a file: the `csv` probe takes
the first line of every source as its header row.

**`ProbeResult`** holds `Tables` (table name -> bareword field -> sample
values), `Variables` (`$`-variable -> sample values) and an optional `Note`
shown with the fields. Formats without tables put their fields under the
table `logformat.ProbeAnyTable` (the empty name). `Probe` adds the format's
`KnownVariables` the sample did not show, without values, to every
non-empty result.

**`ProbeResult.TableVariables`** (table -> `$`-variable -> sample values)
holds the `$`-variables the lines of each table filled, so that a query
`from TABLE` is offered only those, never the `KnownVariables` the lines
did not show.

* The generic probe fills it for every table of its result, also for a
  result of one table.
* Once it is not nil, `Probe` gives every table of `Tables` an entry (empty
  if your `Prober` set none: no variables seen) and drops the entries of
  tables `Tables` does not list.
* A `Prober` may leave it nil: the format then has no per-table data and
  `ProbeResult.VariablesOf(table)` falls back to `Variables`, so results
  built before the field existed stay valid.
* `ProbeResult.FilterTable(table)` returns the part about one table, which
  a table-targeted probe answers.

**`ProbeTabler`** is the optional interface `ProbeLineTable(line string)
(table string, ok bool)`: it tells a line's table without parsing its
fields, cheaply enough to run over every line a probe scans, also those its
sample does not keep. The `default` format implements it.

* A probe adds the tables so found, without fields, to the result
  (`ProbeTableScanner`), so the table list covers the whole scanned window.
* A table-targeted probe keeps exactly the lines a `ProbeTabler` assigns to
  the table (`ProbeTableScanner.Matcher`, names compared cut to
  `ProbeMaxNameBytes`, as they are listed).
* Lines no `ProbeTabler` claims are kept when they hold the table name as a
  word of its own (or as the first `ProbeMaxNameBytes` bytes of a longer
  word, the name such a table is listed under). So a format that names
  tables only in its `Prober` still gets its table probed, but less
  precisely: implement `ProbeTabler` if your format has tables.
* A panic in `ProbeLineTable` is recovered: the format's tabler is not used
  for the rest of the probe, which reports `probe: log format probe failed`
  for the format.

**`Prober`** is the optional interface `Probe(sample ProbeSample)
(ProbeResult, error)`. When the parser your factory returns implements it,
`Probe` calls it instead of the generic probe, like the aggregator uses
`FieldsIntoParser` and `SourceReleaser`. There is no second registry: the
format's one registration covers its probe.

### Writing a probe

A custom probe can call `ProbeWithParser(p, sample)` and adjust the result,
or replace it: to name tables, list a fixed schema without sampling, hide
fields, or supply better sample values. `custom1.go` holds this commented
example:

```go
var _ Prober = (*custom1Parser)(nil)

func (p *custom1Parser) Probe(sample ProbeSample) (ProbeResult, error) {
	result, err := ProbeWithParser(p, sample)
	if err != nil {
		return result, err
	}
	if fields, ok := result.Tables[ProbeAnyTable]; ok {
		delete(fields, "secret")
		delete(result.Tables, ProbeAnyTable)
		result.Tables["ORDERS"] = fields
		result.Note = "custom1: order log, one line per order"
	}
	return result, nil
}
```

What the built-in probes do:

* The `default` probe groups the fields by the `MAPREDUCE:<TABLE>` of each
  line, the table of `from TABLE`.
* A query's table name is upper-cased, so no table name selects a line
  whose table name holds lower-case letters; only a query without a table
  reads it (`mapr.SelectableTable`). The `default` probe lists such a
  line's fields under `ProbeAnyTable` and names the table in the `Note`.
  List only tables a query can name in a probe of your own too.
* The `csv` probe adds the columns of the header rows, also when no data
  row was sampled.

### Rules every probe result follows

**Empty results.** A format whose parser cannot be built (the
`custom1`/`custom2` templates, the `mimecast` stubs, unknown names) or,
with the generic probe, that parses no line of the sample yields an empty
result and no error, so it is left out. A format's own probe decides
itself: the `csv` probe lists the header columns also when no data line
parses, and a result with only a `Note` is not empty.

**Caps.** `Probe` cuts every result, also one your `Prober` built:

| Limit | Value | Constant |
|-------|-------|----------|
| Tables | 64 | `ProbeMaxTables` |
| Fields per table, variables, variables per table | 256 | `ProbeMaxFieldsPerTable` |
| Distinct sample values per field | 5 | `ProbeMaxValuesPerField` |
| Bytes per value, cut at a UTF-8 boundary | 64 | `ProbeMaxValueBytes` |
| Bytes per table, field and variable name | 64 | `ProbeMaxNameBytes` |
| Bytes of note | 256 | `ProbeMaxNoteBytes` |

* Names equal after the cut share one entry.
* The generic probe keeps the first tables and values it sees and, of a
  line's fields that no longer fit, the first in sorted order, so the same
  sample always yields the same result.
* Of a `Prober`'s result the first tables, fields and variables in sorted
  order are kept.
* `TableVariables` entries are only kept for tables the capped `Tables`
  list.

**Errors.** An error is returned only when a `Prober` returns one or the
parser panics during the probe (the panic is recovered).

**Fresh parsers.** Your factory must return a new parser on every call, as
the built-in ones do. `Probe` builds a parser for the probe alone and
releases the sample's sources on it (`SourceReleaser`) afterwards, which
would hit the sources of a parser shared with a running query.

**No embedding.** Embedding another parser promotes its `Probe` (e.g. the
default format's grouping by table) and `ProbeLineTable` onto your type,
the same hazard as for `MakeFieldsInto`: hold a reused parser in a named
field.

### The csv probe's source check

The `csv` probe only probes sources that look like csv files
(`csv_probe.go`):

* The first line must parse as one csv record with at least two columns
  that are neither empty nor longer than `ProbeMaxNameBytes`. Only those
  columns are listed; the others, e.g. after a trailing comma, are left
  out.
* At least half of the sampled data rows must have between two columns and
  the header's number of columns (the parser takes rows with fewer columns
  than the header).
* Once three data rows were sampled, no more than half of them may echo the
  header: hold, in some column whose name has a letter, a value equal to
  that name but for the digits of its numbers. One or two such rows are no
  evidence.

The echo rule tells a header from a log line: a header row names its
columns and so differs from its data, while the first line of a free-text
log is a line like the others (`request 1 done, ok` before `request 2
done, ok`).

Other sources are left out, and a sample without such a source yields an
empty result. So a log of another format (e.g. dserver's own `|`-separated
log) gets no `csv` section in `:fields` and no csv field names in the Tab
completion.

This is a heuristic:

* A csv file of a single column is never probed (`dmap` still queries it).
* Neither is a csv file without a header row most of whose rows repeat a
  value of the first one; its columns would be named after that row's
  values.
* A free-text log most of whose lines hold a comma, no more commas than its
  first line, is probed as csv all the same when in at least half of its
  lines every text between the commas differs from the first line's by more
  than numbers (`user bob logged in, from berlin` before `user alice logged
  out, from paris`).
