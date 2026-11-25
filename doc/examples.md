Examples
========

Working examples for every DTail command. See `--help` of each command for all
options.

The animations are recorded against a demo fleet of six local `dserver`s
(`web01` ... `db01`, listed in `servers.txt`), so their commands read
`logs/app.log` where the text uses real paths. They are rendered from the
scripts in [`tapes/`](tapes/) with `make gifs`.

## Table of contents

* How to use `dtail`
* How to use `dcat`
* How to use `dgrep`
* How to use `dmap`
* Watching the read progress of `dcat`, `dgrep` and `dmap`
* How to use the DTail serverless mode
* Exit statuses of `dcat` and `dgrep`
* More examples

## How to use `dtail`

### Following logs

Follow the logs of many servers at once and print only the lines matching a
regular expression (Go syntax):

```shell
% dtail --servers serverlist.txt --grep INFO --files "/var/log/dserver/*.log"
```

![dtail](dtail.gif "Tail example")

`--files` can be left out:

```shell
% dtail --servers serverlist.txt --grep INFO "/var/log/dserver/*.log"
```

**The server list** is a file or a comma-separated list:

```
# web tier
web01.example.org
web02.example.org:2223
```

```shell
% dtail --servers server1.example.org,server2.example.org:2223 --grep INFO /var/log/app.log
```

* A file has one `HOST` or `HOST:PORT` per line. Blank lines and lines
  starting with `#` are ignored. A `#` after an entry starts no comment.
* Whitespace around an entry, around the commas and around the file's path is
  ignored. So are empty entries and a UTF-8 byte order mark at the start of
  the file.
* A list that names no server is an error (`no server to connect to`), e.g. a
  file of only comment lines or `--servers ' '`. `dtailhealth` reports it as
  `CRITICAL`.
* The same error: a `--discovery` module or a `/regex/` filter that yields no
  server, and an empty `--servers ''` together with `--discovery`.

**The files** are a comma-separated list of files or globs:

* Blanks around each entry are removed, and a path containing a comma cannot
  be read.
* A path containing spaces works when quoted for the shell, e.g.
  `--files '/var/log/my app/*.log'`.

### Aggregating logs

Add a MapReduce query to aggregate the lines written from now on. The result
is printed every 5 seconds (the query's default `interval`) whenever it
changed. For lines written in the past use `dmap`.

```shell
% dtail --servers serverlist.txt \
    --files '/var/log/dserver/*.log' \
    --query 'from STATS select sum($goroutines),sum($cgocalls),last($time),max(lifetimeConnections)'
```

![dtail-map](dtail-map.gif "Tail mapreduce example")

`--query` can be left out too. A query with `group by` and `order by`:

```shell
% dtail --servers serverlist.txt \
    --files '/var/log/dserver/*.log' \
    'from STATS select $hostname,max($goroutines),max($cgocalls),$loadavg,lifetimeConnections group by $hostname order by max($cgocalls)'
```

![dtail-map](dtail-map2.gif "Tail mapreduce example 2")

DTail must support your log format for a query to work. See the
[query language](./querylanguage.md) and the [log formats](./logformats.md).

Write the result to a CSV file with `outfile`:

```shell
% dtail --servers serverlist.txt \
    --files '/var/log/dserver/*.log' \
    --query 'from STATS select ... outfile result.csv'
```

* `outfile result.csv` always holds the result so far.
* `outfile append result.csv` appends the result once, when `dtail` ends or an
  interactive reload replaces the query. Interim results are not appended.
  Until then the result is only in memory, so a killed or crashed `dtail`
  appends nothing.

The [query language](./querylanguage.md) describes `outfile` in full.

## How to use `dcat`

Display the full content of files on many servers at once:

```shell
% dcat --servers serverlist.txt --files /etc/hostname
```

![dcat](dcat.gif "Cat example")

`--files` can be left out:

```shell
% dcat --servers serverlist.txt /etc/hostname
```

Output and logging flags, which work with every DTail client:

| Flag | Effect |
|---|---|
| `--plain` | Prints the bare lines, without DTail's line prefix. |
| `--noColor` | Prints without terminal colors. |
| (default logger `fout`) | Diagnostics (`INFO`/`WARN`/`ERROR` lines) go to stdout and to the daily file `~/log/YYYYMMDD.log`. The payload goes to stdout only. |
| `--log-payload` | Also tees the payload into the daily log file (config: `Client.LogPayload`). |
| `--logger stdout` | Logs to stdout only. |
| `--logger none` | Writes no log file. |
| `--logger file` | Logs to the log file only, payload included. In serverless mode the payload still goes to stdout. |

## How to use `dgrep`

Display only the lines matching a regular expression. Here the server list is
given on the command line, as one server with its port:

```shell
% dgrep --servers server1.example.org:2223 \
    --files /etc/passwd \
    --regex nologin
```

![dgrep](dgrep.gif "Grep example")

`--grep` is an alias for `--regex`. Context and limits work as with `grep`
(the animation shows them); `dgrep` is a handy way to search old application
logs:

```shell
% dgrep --servers serverlist.txt --before 2 --after 2 --max 10 \
    --grep ERROR '/var/log/app/*.log'
```

## How to use `dmap`

Run a MapReduce query over logs written in the past. `dmap` prints interim
results every 5 seconds (the default `interval`) whenever they changed, and
the final result once all files were read.

```shell
% dmap --servers serverlist.txt \
    --files '/var/log/dserver/*.log' \
    --query 'from STATS select $hostname,max($goroutines),max($cgocalls),$loadavg,lifetimeConnections group by $hostname order by max($cgocalls)'
```

![dmap](dmap.gif "DMap example")

Add `outfile result.csv` to the query to write the result to a CSV file.

DTail must support your log format: use one from `internal/mapr/logformat` or
add your own, see the [log formats](./logformats.md). The example works out of
the box, as DTail understands its own log format.

### The query builder

Without a query, `dmap` on a terminal opens a query builder instead of
failing:

```shell
% dmap --servers serverlist.txt --files '/var/log/dserver/*.log'
No query given: build one and press Enter to run it. Tab guides through the query, :fields lists the tables and fields found, :help helps, :quit leaves without running.
query> from STATS select $hostname,max($goroutines) group by $hostname
```

![dmap-query-builder](dmap-query-builder.gif "The dmap query builder")

* It probes the files for their tables and fields. Tab guides you through the
  query (`from`, the table, `select`, ...) and `:fields` lists what was found.
* Enter runs the query exactly as `--query` would have, then `dmap` exits.
  `:quit` leaves without running anything.
* In a pipe, a script or cron, and with `--no-interactive`, a missing query is
  still an error.

See [interactive-query.md](interactive-query.md).

## Watching the read progress of `dcat`, `dgrep` and `dmap`

Every `dserver` reports how many bytes of its files it has read, and the
client sums that up. By default the progress is part of the `STATS` lines,
printed every 3 seconds while something changes. `--quiet` and `--plain` drop
those lines.

With `--progress` the client writes `PROGRESS` lines to stderr every second
instead, whatever `--quiet` or `--plain` say, so stdout keeps the data only:

```shell
% dgrep --servers serverlist.txt --files '/var/log/app/*.log.gz' --regex ERROR \
    --plain --progress > errors.txt
PROGRESS|bar=[######--------------]|progress%=30.0|progress=1.20GiB/4.00GiB|rate=85.30MiB/s|eta=35s|files=3/10|reporting=2/2|bytes=1288490188|total=4294967296
...
PROGRESS|bar=[####################]|progress%=100.0|progress=4.00GiB/4.00GiB|rate=86.10MiB/s|eta=0s|files=10/10|reporting=2/2|bytes=4294967296|total=4294967296
```

`journal:` units, stdin and `dtail` following files report no progress. The
fields, and what is counted after `--max` or an error, are described in
[read-progress.md](./read-progress.md).

## How to use the DTail serverless mode

Leave the server list out and the client reads local files itself: no
`dserver`, no SSH, no network. Every command works that way.

![serverless](serverless.gif "Serverless mode")

* `--servers serverless` says the same explicitly (in any case, whitespace
  around it ignored).
* The serverless mode logs at level `warn` unless you say otherwise, e.g.
  `--logLevel info`.

### Serverless map-reduce query

The `dmap` example from above on a local file, with flags, in the short form
and reading from a pipe:

```shell
% dmap --files /var/log/dserver/dserver.log \
    --query 'from STATS select $hostname,max($goroutines),max($cgocalls),$loadavg,lifetimeConnections group by $hostname order by max($cgocalls)'
```

```shell
% dmap 'from STATS select $hostname,max($goroutines) group by $hostname' /var/log/dserver/dserver.log
```

```shell
% cat /var/log/dserver/dserver.log | \
    dmap 'from STATS select $hostname,max($goroutines) group by $hostname'
```

### Aggregating CSV files

This works like aggregating logs. The first line of every CSV file must be the
header. `dmap` can query CSV files on remote servers too.

```shell
% cat example.csv
name,lastname,age,profession
Michael,Jordan,40,Basketball player
Michael,Jackson,100,Singer
Albert,Einstein,200,Physician
% dmap --query 'select lastname,name where age > 40 logformat csv outfile result.csv' example.csv
% cat result.csv
lastname,name
Einstein,Albert
Jackson,Michael
```

### Other serverless commands

```shell
% dtail /var/log/dserver/dserver.log
% dtail --logLevel trace /var/log/dserver/dserver.log
% dcat /etc/passwd
% dgrep --regex ERROR --files /var/log/dserver/dserver.log
% dgrep --before 10 --after 10 --max 10 --grep ERROR /var/log/dserver/dserver.log
```

`--plain` output is the file, byte for byte:

```shell
% dcat --plain /etc/passwd > /tmp/passwd.copy
% diff /tmp/passwd.copy /etc/passwd    # shows no difference
```

### Server warnings and log levels

A server sends warnings to the client: a missing file, a glob matching more
files than the server reads, a split long line, and so on.

* **`dserver`'s `--logLevel` does not filter them.** A `dserver` at
  `--logLevel error` or `none` still sends them with their full text. Its
  level only decides what `dserver` writes to its own log.
* **In server mode the client's `--logLevel` does not filter them either.**
  The client prints them as output lines: `SERVER|<host>|WARN|...` by default,
  the bare `WARN|...` line with `--plain`. So `dcat --plain` output redirected
  to a file can contain them.
* **In serverless mode the client's `--logLevel` filters them.** The client
  reads the files itself and its warnings go through its own logger, which
  defaults to `warn` there.

## Exit statuses of `dcat` and `dgrep`

| Status | Meaning |
|---|---|
| `0` | Every server read what was asked. `dgrep` exits `0` also when no line matched (unlike `grep`). |
| `1` | A connection or session failed: server unreachable, SSH handshake failed, another protocol version, a session cut short (e.g. a killed `dserver`), or the client could not start. |
| `2` | A server reported a failed command (a missing, denied or unreadable file, ...), or the command line had an invalid flag. |

With several servers the highest status wins. `dtail`, `dmap` and
`dtailhealth` keep their own statuses.

```shell
% dcat --servers serverlist.txt --files /var/log/missing.log; echo $?
...
2
```

Every case is listed in [exit-statuses.md](./exit-statuses.md).

## More examples

### Journal, gzip, hosts and users

```shell
% dtail --servers serverlist.txt journal:myapp.service
# Follows a systemd unit's journal (Linux servers with journalctl on PATH,
# capability journal-v1)
```

```shell
% dtail --servers serverlist.txt /var/log/app/app.log.gz
# Follows a gzip file (.gz, .gzip): waits at its end and prints the lines of
# the gzip members appended from then on (the writer must append whole members)
```

```shell
% dcat --servers serverlist.txt --trustAllHosts /etc/hostname
# Skips the unknown-host trust prompt (use with care)
```

```shell
% dtail --servers serverlist.txt --user paul --files '/var/log/dserver/*.log'
# Connects as SSH user "paul" instead of the local system user
```

```shell
% dcat --servers serverlist.txt --log-payload /etc/hostname
# Also tees the retrieved payload into the daily client log file ~/log/YYYYMMDD.log
```

### Interactive mode

`dtail` in a terminal is interactive by default (`--no-interactive` turns it
off). Press Enter to pause the output, type `:help` for examples, and change
the filter without reconnecting:

```shell
% dtail --servers serverlist.txt --files '/var/log/dserver/*.log'
:reload --grep WARN
```

The other clients need `--interactive-query`:

```shell
% dgrep --servers serverlist.txt --files '/var/log/dserver/*.log' --grep ERROR --interactive-query
:reload --grep WARN
```

An interactive `dtail` switches between following lines and a MapReduce query
on the same session. Tab guides the query from the tables and fields found in
the logs:

```shell
% dtail --servers serverlist.txt --files '/var/log/dserver/*.log'
:query <Tab>                       # inserts "from STATS " (the table found)
:query from STATS select <Tab>     # lists the aggregations, fields, $variables
:query from STATS select $hostname,count($line) group by $hostname interval 10
:tail                              # back to following lines
:fields                            # the tables and fields the servers' logs carry
```

* The first result table shows about a second after every server's first
  results arrived (one `interval` after the query started).
* Alt-E opens the line in `$EDITOR`, and `--edit-mode vi` adds vi keys.
* A `dtail` started without `--files` reads nothing and says so. `:reload
  --files PATH` gives it files (Tab lists the remote paths).

See [interactive-query.md](interactive-query.md) for the guide and
[interactive-query-reference.md](interactive-query-reference.md) for every
rule.

### Regular expressions

```shell
% dgrep --servers serverlist.txt --invert --grep INFO --files '/var/log/dserver/*.log'
# Prints all lines NOT matching the regex
```

A regex is matched against the line without its line terminator, for every
kind of read (`dtail`, `dcat`, `dgrep`, `dmap`, journal targets).

| Pattern | Selects |
|---|---|
| `--regex 'foo$'` | The lines ending in `foo`. |
| `--regex '^$'` | The empty lines (`dgrep`). A line ending in `\r\n` is not empty. |
| `.` (the default of `dtail` and `dgrep`), `.*`, the empty pattern | Every line, empty lines included. |
| `--invert --grep '.*'` | No line. With `--invert` these patterns are matched like any other regex, as `grep -v` does. |
| `--invert --grep .`, or `--invert` without a regex | Only the empty lines. |

Where the empty pattern can be given:

| Given as | Result |
|---|---|
| `--grep ''` on the command line | Ignored: the default `.` stays. |
| `dgrep --regex ''` | Rejected (`no regex specified`). |
| `dtail --regex ''`, `:reload --regex ''` (or `--regex=''`) | Used. Inverted it prints no line, like `.*`. |
| `:reload --grep ''` | Rejected, with a hint to use `--regex ''`. |

A `:reload` keeps an earlier `--invert` (from the command line or a previous
reload) unless it gives `--invert=false`. So after `--invert`, a `:reload
--regex ''` prints no line. `:show` prints the current `invert=` value.

### `dtail --query` and the line filter

* With `--query`, `dtail` reads the lines of the query's table, whatever
  `--regex` or `--grep` say. For `from .` or no table it reads every line.
* It ignores `--invert` with a warning. Inverting the table filter would feed
  the other tables' lines, or only the empty lines, into the aggregation.
* An interactive `dtail --query` (the default on a terminal) prints a one-line
  note instead. It keeps `--regex`/`--grep`, `--invert`, `--before`, `--after`
  and `--max` for following lines, which its session can switch to, and sends
  none of them with the query.
* In an interactive query session (`dmap` or `dtail --query` with
  `--interactive-query`) `:reload --invert` is rejected with the same reason,
  and the current workload keeps running.

See "How it works" in
[interactive-query-reference.md](interactive-query-reference.md).

### Other client options

An option given on the command line overrides the config file, also when it
names the default value (`--port 2222`, `--logLevel info`).

| Option | Effect |
|---|---|
| `--cfg FILE` | Reads another config file (default `~/.config/dtail/dtail.conf`, else `~/.dtail.conf`). |
| `--port N` | SSH port of the servers given without `:PORT` (default `2222`). |
| `--known-hosts-path FILE`, `--trustAllHosts`, `--trustChangedHostKeys` | Control the host key check, see the [Quick Starting Guide](quickstart.md). |
| `--auth-key-path FILE` (deprecated alias `--key`), `--no-auth-key`, `--agentKeyIndex N` | Select the SSH keys, see [auth-key-fast-reconnect.md](auth-key-fast-reconnect.md). |
| `--cpc N` | How many connections are established per CPU core at a time (default `10`). |
| `--hostname-override NAME` | The host name the client uses in its log lines and output. |
| `--quiet` | Suppresses the periodic connection `STATS` lines. |
| `--progress` | Writes the read progress to stderr, see [read-progress.md](./read-progress.md). |
| `dtail --shutdownAfter N` | Ends `dtail` after `N` seconds (default `86400`). |
| `--timeout N` (`dtail`, `dmap`) | Asks the servers to stop reading after `N` seconds (`dtail` also ends then). |
| `--interactive-query` (all clients), `--no-interactive` (`dtail`, `dmap`), `--control-tty DEV` (default `/dev/tty`), `--edit-mode emacs\|vi` | Set up the interactive control, see [interactive-query.md](interactive-query.md). |
| `--logger`, `--logDir`, `--logLevel`, `--log-payload` | Choose the client's logging, see "How to use `dcat`" above. |
