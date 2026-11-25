# MapReduce query language

Used by `dmap --query`, `dtail --query`, interactive `:query`, and
`dserver` jobs. Quote the whole query in single shell quotes.

## Grammar

A query is a list of clauses in **any order**; only `select` is required.
Each clause runs up to the next clause keyword.

```text
select SELECT[,SELECT...]          SELECT := FIELD | AGGREGATION(FIELD)
from TABLE                         e.g. STATS for lines with |MAPREDUCE:STATS|
where COND[, | and COND...]        COND := ARG OP ARG; AND-ed, there is no `or`
group [by] FIELD[,FIELD...]
order [by] ORDERFIELD [desc]       descending
rorder [by] ORDERFIELD [asc]       ascending
set $VAR = VALUE[,...]             VALUE := FLOAT|"STRING"|FIELD|FUNC(FIELD); blanks around =
interval SECONDS                   default 5; 0 means 1
limit N
outfile [append] PATH
logformat NAME
```

- Clause keywords: `select from where set group rorder order interval
  limit outfile logformat` (case-insensitive).
- Aggregations (lower case only): `count`, `sum`, `min`, `max`, `avg`,
  `last`, `len` (length of the last value), `percentage` (group's share of
  the field's total, 0-100), `percentile` (rank of the group's value among
  all groups, 0-100).
- Numeric operators: `==`, `!=`, `<`, `<=` (`=<`), `>`, `>=` (`=>`);
  operands are fields or numbers, never quoted strings.
- String operators (names in any case; comparisons are case-sensitive):
  `eq`, `ne`, `contains`,
  `ncontains` (alias `lacks`), `hasprefix`, `nhasprefix`, `hassuffix`,
  `nhassuffix`.
- `set` functions: `md5sum(FIELD)`, `maskdigits(FIELD)` (they nest); e.g.
  `set $user = maskdigits(user) select $user,count($user) group by $user`.
  `set` runs after `where`.

## Fields

- **Barewords** are values the log format extracts from the line, e.g.
  `service`, `latency` from `service=web|latency=12`. Never prefix them
  with `$`: an unknown `$name` has no value (grouped by: one empty group;
  selected: no row; in `where`: matches no line) and the client only
  warns.
- **`$variables`** are set by DTail: everywhere `$hostname`/`$server`,
  `$line`, `$empty`, `$timezone`, `$timeoffset`; with the `default`
  format also `$time`, `$date`, `$hour`, `$minute`, `$second`,
  `$severity`/`$loglevel`, `$caller`, `$pid`, `$goroutines`, `$cgocalls`,
  `$cpus`, `$loadavg`, `$uptime`. See [log-formats.md](log-formats.md).
- `*` as in `count(*)` groups everything into one row.
- A field named like a keyword, aggregation or number is written in
  backticks: `` select `from`,`count($foo)` ``. A quoted or backticked
  keyword starts no clause.
- A bare number in a string comparison is a literal (`where status eq
  500`); quoted text is a string; other barewords are fields. In
  `select`/`group by` a quoted word is a field name.
- A condition on a field the line lacks, or a numeric comparison on a
  non-number, is false, even for `ne`, `ncontains` and `!=`.

## Tables

- `from TABLE` reads lines holding `|MAPREDUCE:TABLE|` and selects the
  `default` parser. Table names are upper-cased (`from stats` = `STATS`)
  and matched literally (no regex).
- `from *` reads every `MAPREDUCE` line (also lower-case table names).
- No `from` reads **every** line and, without `logformat`, uses the
  limited `generic` parser (common variables only, no barewords).
- `from .` reads every line too but keeps the configured parser
  (`default`, which parses only DTail's own `MAPREDUCE` lines), so on other
  logs it returns nothing unless a `logformat` is given.
- A query with `logformat csv` and no `from` reads every line.
- For non-DTail logs use `logformat generickv`, `csv` or `generic`.

## Rules the parser enforces

- Without `group by`, the first selected field is the group (so
  `select count($line)` alone counts each distinct line; use `count(*)` for
  one total). A plain selected field means `last(field)`.
- `order`/`rorder` take one field that must also be selected;
  `order by x asc` is rejected (use `rorder`).
- Each select item at most once (`count(x),count(x)` rejected); different
  aggregations of one field are fine.
- One value per `from`, `order`, `interval`, `limit`, `logformat`,
  `outfile`; an extra word is an error (`unexpected word "gro" after the
  value of the 'limit' clause`).
- `select`/`group by` items may be separated by blanks; a bare `by` there
  is rejected (typo guard).
- `limit` >= 0; `interval` needs a number.
- `NaN`/`Inf` and non-numbers are skipped by numeric aggregations (logged
  as aggregation errors).

## Semantics to remember

- Numeric aggregations use only the group's lines that have the field with
  a numeric value; `avg` weighs every value equally across servers.
- Multiple group fields form a tuple key.
- Rows are sorted by group key unless ordered; the printed table shows at most
  **10 rows** unless `limit` is given (outfiles get all rows). `count`
  prints as an integer, other aggregations with six decimals.
- The client rejects an invalid query before connecting (`invalid query:
  ...`); dserver rejects it too (`map: invalid query`).
- `dmap` has no `--regex`; filter with `where`.

## Examples

```sh
dmap --servers s.txt --files '/var/run/dserver/log/*.log' --no-interactive \
  --query 'from STATS select $hostname,max($goroutines),avg($cgocalls) group by $hostname order by max($goroutines) limit 10'
dmap --files '/var/log/app/*.log' --no-interactive \
  --query 'from REQUESTS select service,count(*),avg(latency),percentile(latency) where status >= 500 group by service rorder by avg(latency)'
dmap --files access.log --query 'select $line,count($line) where $line contains "timeout" logformat generic'
dtail --servers s.txt --files '/var/log/app/*.log' --shutdownAfter 120 --no-interactive \
  --query 'from REQUESTS select service,count(*) group by service interval 10'
```

Full reference: `doc/querylanguage.md`. Results, intervals and outfiles:
[mapreduce-results.md](mapreduce-results.md).
