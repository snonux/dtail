DTail Query Language
====================

The query language runs SQL-like MapReduce queries over log files: with
`dmap`, with `dtail --query`, and in scheduled and continuous server jobs.
This page is its reference.

```shell
% dmap --files /var/log/dserver/dserver.log \
    --query 'from STATS select $hostname,max($goroutines),lifetimeConnections group by $hostname'
```

DTail must understand the log format to extract fields; see
[log formats](./logformats.md). More examples are on the
[examples](./examples.md) page. In an interactive session `:query <query>`
runs a query and Tab completes it; see
[interactive queries](./interactive-query.md).

## Syntax

A query is a sequence of clauses in **any order**. Only `select` is required.

```shell
QUERY := CLAUSE1 [CLAUSE2 ...]
CLAUSE := select SELECT1[,SELECT2...]
          | from TABLE
          | where CONDITION1[,CONDITION2...]
          | group [by] FIELD1[,FIELD2...]
          | order [by] ORDERFIELD [desc]
          | rorder [by] ORDERFIELD [asc]
          | set SET1[,SET2...]
          | interval NUMBER
          | limit NUMBER
          | outfile [append] STRING
          | logformat LOGFORMAT
```

... whereas:

```shell
TABLE := The mapreduce table name, e.g. STATS in MAPREDUCE:STATS (see "Special
         table names" below)
SELECT := FIELD|AGGREGATION(FIELD)
CONDITION := ARG1 OPERATOR ARG2
ARG := FIELD|FLOAT|STRING
OPERATOR := FLOATOPERATOR|STRINGOPERATOR
FLOATOPERATOR := One of: == != < <= =< > >= =>
STRINGOPERATOR := eq|ne|contains|ncontains|lacks|hasprefix|nhasprefix|hassuffix|nhassuffix
ORDERFIELD := FIELD|AGGREGATION(FIELD)
SET := $VARIABLE = FLOAT|STRING|FIELD|FUNCTION(FIELD)|FUNCTION(FUNCTION(FIELD))...
LOGFORMAT := default|generic|generickv|csv|... (case sensitive)
AGGREGATION := count|sum|min|max|avg|last|len|percentage|percentile
FUNCTION := md5sum|maskdigits
```

The types:

```shell
NUMBER := A whole number (e.g. 42)
FLOAT := A float number, e.g. 3.14
STRING := A quoted string, e.g. "foo"
FIELD := BAREWORD|$VARIABLE
BAREWORD := A bare string without quotes, e.g. foo. A value extracted from the
            log line.
$VARIABLE := Like a bareword, but with a $ prefix, e.g. $foo. A value set by
            DTail itself (e.g. $hostname) or by a set clause.
```

Clause keywords and where operators are case insensitive (`SELECT`, `EQ`).
Aggregation and function names are lower case only (`COUNT(foo)` is
rejected). A unit test keeps the grammar above in step with the parser.

## Clauses

| Clause | Meaning |
|--------|---------|
| `select a,count(b)` | The result columns: fields and aggregations of fields. Each may appear only once. |
| `from TABLE` | Read only the lines of this table (see "Special table names"). |
| `where a > 1, b eq "x"` | Count only lines that satisfy every condition. |
| `group by a,b` | One result row per distinct value tuple. `by` is optional. |
| `order by x [desc]` | Sort by a select field, descending. |
| `rorder by x [asc]` | Sort by a select field, ascending. |
| `set $v = f(a)` | Set a variable on every line that passed `where`. |
| `interval N` | Report results every N seconds (default 5; `0` means 1). |
| `limit N` | Print or write at most N rows. |
| `outfile [append] PATH` | Write the result as CSV to `PATH`. |
| `logformat NAME` | Parse the lines with this [log format](./logformats.md). |

## Aggregations

| Aggregation | Result per group |
|-------------|------------------|
| `count(f)` | Number of lines that have the field. |
| `sum(f)` | Sum of the field's values. |
| `min(f)`, `max(f)` | Smallest and largest value. |
| `avg(f)` | Sum divided by the number of numeric values. |
| `last(f)` | The most recently received value. A plain select field is `last`. |
| `len(f)` | Length of the most recently received value. |
| `percentage(f)` | The group's share of the field's total over all groups, in percent. |
| `percentile(f)` | Percentile rank (0-100) of the group's value among all groups; equal values share a rank. |

`count` prints a whole number, `last` the value as received, the others six
decimals. Only lines that have the field with a numeric value count; a value
that is no finite number is logged as an aggregation error. See
"Missing fields" under Details.

## Where operators

| Operator | Compares | True when |
|----------|----------|-----------|
| `==` `!=` | numbers | equal, not equal |
| `<` `<=` `>` `>=` | numbers | less, less or equal, greater, greater or equal |
| `=<` `=>` | numbers | aliases for `<=` and `>=` |
| `eq` `ne` | strings | equal, not equal |
| `contains` `ncontains` | strings | left holds right, or does not |
| `lacks` | strings | alias for `ncontains` |
| `hasprefix` `nhasprefix` | strings | left starts with right, or does not |
| `hassuffix` `nhassuffix` | strings | left ends with right, or does not |

* Conditions are separated by commas, blanks or `and`, and are AND-ed.
  There is no `or`.
* Number operators take barewords only (numbers, or fields holding
  numbers): `where bytes > "3"` is rejected.
* String operators take fields, quoted strings and bare numbers:
  `where status eq "ok"`, `where code eq 404`. An unquoted word is a field
  name, so `status eq ok` compares with the field `ok`.
* A condition on a field the line lacks is false, also for the negating
  operators: `where status ne "ok"` skips lines without `status`. So is a
  number comparison of a value that is no number.

## Set functions

| Function | Result |
|----------|--------|
| `md5sum(f)` | Hex MD5 checksum of the value. |
| `maskdigits(f)` | The value with every digit `0`-`9` replaced by `.`. |

```shell
% dmap --files app.log --query 'select $m,count($line) set $m = maskdigits($line) group by $m'
```

* `set` runs after `where` and before grouping: the variable can be
  selected and grouped by, but a `where` condition on it is false.
* The `=` must stand apart (`set $a = $b`, not `set $a=$b`) and the name
  must start with `$`.
* Functions nest, the inner one first: `set $h = md5sum(maskdigits($line))`.
* The right side is a number, a quoted string, a field or a function of a
  field. A field the line lacks is taken as its literal text.

## Fields and quoting

Dynamic `key=value` fields of a line are barewords (`service`, `bytes`);
`$`-variables are set by DTail (`$hostname`, `$line`, ...) or by `set`. Which
exist depends on the [log format](./logformats.md).

* Outside quotes a comma is the same as a blank: `select a b` is
  `select a,b`.
* In `select`, `group by`, `order by` and `from` a quoted word is a field or
  table name, which may hold blanks and commas (`select "foo bar"`).
* In `where` and `set` a quoted word is a string literal.

### Escaping keywords

Backticks make a word a literal field name:

```shell
% dmap --files example.log --query 'select `count($foo)` group by `count($foo)`'
```

This selects the field literally named `count($foo)` instead of counting
`$foo`. The same way a field can be named like a clause keyword
(`` `from` ``), like `by` (`` `by` ``) or like a number (`` `1` ``).

## Special table names

`from TABLE` reads the lines holding `|MAPREDUCE:TABLE|`. The name is
upper-cased, quoted or not (`from stats` reads `STATS`), and taken
literally: `from A.B` does not read `|MAPREDUCE:AXB|`.

| Table | Lines read | Log format without a `logformat` clause |
|-------|------------|------------------------------------------|
| `from NAME` | Lines of the table `NAME`. | The configured one (`default`). |
| `from *` | Every `MAPREDUCE` line, whatever its table. | The configured one. |
| `from .` | Every line. | The configured one. |
| no `from` | Every line. | `generic`. |

* A table name with lower-case letters in the log (`|MAPREDUCE:stats|`)
  matches no `from NAME`; only `from *`, `from .` and no `from` read it.
* `logformat csv` without `from` behaves like `from .`.
* The table alone selects the lines: `dmap` has no `--regex`, and
  `dtail --query` ignores its `--regex`/`--grep` and `--invert` (with a
  warning for `--invert`). Narrow down with `where`.

## Selecting the log format and dynamic fields

Two things commonly make a field "not resolve":

1. **`key=value` fields are barewords, not `$`-variables.** For
   `...|service=web|bytes=100` write `select service,sum(bytes)`, not
   `$service`. An unknown `$name` has no value: grouped by, it is the empty
   string; selected, it adds no row; in `where` it matches no line. The
   client warns on stderr, e.g. `warning: $service is not a known variable
   for log format "default"; did you mean bareword service?`.
2. **Without `from`, the log format is `generic`**, which has only the
   common variables (`$line`, `$hostname`, ...): no `key=value` fields, no
   `$time`. Use `from STATS` (or any `from`) for DTail's own logs, or name
   the format: `logformat generickv`.

A `logformat` name is case sensitive. For a name without a parser
(`logformat CSV`) dserver logs an error and falls back to `generic`.

## Results

* **Grouping.** Without `group by` the query groups by the first select
  field. `select count(*)` is one group; `select count($line)` alone counts
  every distinct line on its own.
* **Order.** Rows are sorted by the group key unless `order` (descending)
  or `rorder` (ascending) names one select field. `order by x asc` is
  rejected: use `rorder`.
* **Limit.** Printed results show at most 10 rows without `limit` (a
  warning names the number of rows). An outfile gets all rows unless
  `limit` is given.
* **Interval.** Servers send their aggregates once per `interval`, and the
  client reports (prints, or writes the outfile) about as often, plus once
  at the end. A printed table that is empty or unchanged is not repeated.
* **Outfile.** `outfile PATH` writes the result as CSV, with the select
  fields as header, and the query to `PATH.query`. The client writes it
  (for server jobs the scheduling dserver). `outfile append PATH` appends
  instead of replacing.

```shell
% dmap --files /var/log/dserver/dserver.log \
    --query 'from STATS select $hour,count($line),avg($goroutines) group by $hour rorder by count($line) limit 5 outfile result.csv'
```

## Details

### Strict parsing

* A query starts with a bare clause keyword. A quoted (`"select"`) or
  backticked keyword starts no clause and is rejected (`unexpected word
  "select"`).
* `from`, `order`, `rorder`, `interval`, `limit`, `logformat` and `outfile`
  take one value. A further word is rejected by name, e.g. `unexpected word
  "gro" after the value of the 'limit' clause`. Older releases ignored such
  words; see the upgrade note in [installation](./installation.md).
* `order` and `rorder` take one field, which must be in `select`. The
  matching direction word (`order ... desc`, `rorder ... asc`) changes
  nothing; the other one is rejected.
* In `select` and `group by` every word is a field, so `select count(x)
  gro` selects the field `gro`. Only a bare `by` is rejected there
  (`select count(x) gropu by x`), as it is almost certainly a typo.
* `limit` must not be negative, `interval` needs a non-negative number, and
  nothing may follow an aggregation's `)` (`count(foo)bar`).
* A where or set condition missing a part is rejected.

### Select fields are unique

`select count($line),count($line)` fails with `'select' clause has
'count($line)' more than once`. Every select field is a result column under
its text as written, so two equal fields would share one value. For the
same reason `` `count($foo)` `` cannot be selected with `count($foo)`.
Different aggregations of one field are fine.

The client rejects such a query before it connects; a job logs the error
and writes no outfile; a `:reload` is rejected and the running query goes
on. dserver rejects it too, should a client send it anyway, and sends no
results:

* On the plain command stream it reports the failed map command (`map:
  invalid query`), rejects the session's read commands the same way and
  closes the session normally.
* With `--interactive-query` it rejects the session (`session request
  rejected: invalid session spec: ...`).

### Outfile

* Without `append` the result is written to `PATH.tmp` and renamed to
  `PATH`. With `append` a CSV header is written only when the file is new
  or empty, and `PATH.query` is replaced.
* A relative `PATH` is relative to the client's working directory.
* Only the bare, lower case word `append` before a path is the append mode.
  `outfile append` alone, `outfile "append"` and ``outfile `append` `` write
  the file `append`; a word after a quoted or escaped `append` is rejected.
* A path starting with `append:` is rejected. Write a file really named so
  as `outfile ./append:result.csv`.

What `append` appends depends on how the query runs:

| Run as | Interim reports | Appended |
|--------|-----------------|----------|
| `dmap`, `dtail --query` with outfile | Hold the whole result so far; append nothing. | The final result, once, when the query ends. |
| Scheduled job | None. | The final result, once per run. |
| Continuous job | Each holds one interval's rows. | Every interval's rows, once. |

For `dmap` and `dtail --query`:

* The interim reports only check that `PATH` can be opened for appending
  (creating it empty) and log an error every interval if not. They write no
  `PATH.query`.
* The query ends when the client exits or an interactive `:reload`,
  `:query` or `:tail` replaces it, also a `:reload` that keeps the query
  text. The result so far is appended then, and the new query appends its
  own at its end.
* Until then the result lives only in memory: a SIGKILL, crash, forced exit
  or failed final write loses all of it. Without `append` every interim
  report replaces `PATH`, so at most the last interval is lost.

### Interval and report timing

* Every dserver sends what it aggregated since its last send once per
  interval, and the rest once its input ends (a `dmap` read reached the end
  of its files). A query stopped or replaced before that sends no rest.
* The client reports first half an interval after the query started (also
  after `:reload` or `:query`), then once per interval, and once at the
  end.
* The first aggregates move that first report. It waits until every server
  still connected sent its first aggregates and comes one second after the
  last, but at most half an interval after the first server's.
* A connected server that sends nothing (no matching lines) so delays the
  first table by up to half an interval, or until about a second after its
  connection ended. A server trailing by more is in the next table.
* Every such wait is at most half an interval, so `interval 1` reports half
  a second after the sends.
* Later reports follow once per interval, each planned from when the
  previous one was due, however long that took.
* Continuous jobs report every interval after the first half interval.
  Scheduled jobs report no interim results and write their outfile once.

Servers and client run on their own clocks, so a reported result can lack
up to about one interval of the servers' latest lines.

### Missing fields

An aggregation only sees the lines of a group that have its field. A line
with none of the select fields adds no row; a group field the line lacks
counts as the empty string.

| Aggregation | Group where no line has the field | Note |
|-------------|-----------------------------------|------|
| `count`, `sum`, `min`, `max` | `0` | Only lines with the field are used. |
| `last` | empty | |
| `len` | `0` | |
| `avg` | `0` | Lines lacking the field, or with a non-number, do not lower it. |
| `percentage` | `0` | The group adds nothing to the total. |
| `percentile` | `0` | The group takes no part in the ranking. |

* Over the three lines `bytes=10|status=ok`, `status=ok` and
  `bytes=abc|status=ok` of one group, `avg(bytes)` is `10` and
  `count(status)` is `3`.
* dserver sends the number of values with each sum and the client adds up
  both before dividing, so `avg` over several servers weighs every value
  equally.
* `NaN` and `Inf` are not aggregated by `sum`, `avg`, `min`, `max`,
  `percentage` and `percentile`; they are logged as aggregation errors.
* With mixed positive and negative values `percentage` can fall outside
  0-100.

### Cumulative and non-cumulative results

The client merges the servers' results field by field; a result whose lines
all lack a field leaves that field's value as it is.

* **Cumulative** (`dmap`, `dtail --query` with an `outfile`, scheduled
  jobs): all servers and all intervals so far are merged.
* **Non-cumulative** (`dtail --query` without an `outfile`, continuous
  jobs): each result starts from zero and shows the lines received since
  the previous one. A group whose lines all lacked a field in that time
  shows it as if no line had it (e.g. `min` `0`).

Non-cumulative rows are not strictly one interval's lines, as lines can
arrive one result late:

* A dserver puts the groups of an interrupted send back into its next
  interval, and aggregates lines it still batches with a later one.
* A server's result the client could not merge at once joins the global
  result a moment later (retried after 10 ms), possibly after the report.

### Group keys and special characters

* Multiple group fields form a tuple: `("x,y", "z")` and `("x", "y,z")`
  are distinct groups. Compound keys escape commas and percent signs
  internally; selected values are unchanged, and the order of tied rows can
  differ.
* The character `∥` (U+2225) separates the parts of a result on the wire.
  In group keys and in `last()`/`len()` values (plain select fields
  included) it is replaced by `||`.

### Interactive sessions

* `:query <query>` takes the query as typed, without shell quoting. In
  `dtail` it switches from following lines to the query and `:tail`
  switches back. `:reload --query '...'` changes the query too.
* Tab completes the language and the tables, fields, `$variables` and
  sample values a background probe found, `from` and the table first;
  `:fields` lists them. Queries are shown highlighted.
* The field catalog lists only tables a query can name. Fields of lines
  with a lower-case table name appear without a table (`fields:` in
  `:fields`, offered by Tab only while the query names no table).
* The completion reads a partial query with the parser's own tokenizer and
  clause rules (`AnalyzePartial`, `internal/mapr/complete.go`).

See [interactive queries](./interactive-query.md), and "Custom probes" in
[log formats](./logformats.md) for how a log format describes its fields.
