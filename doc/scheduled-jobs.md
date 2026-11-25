DTail Scheduled and Continuous Jobs
===================================

`dserver` can run MapReduce jobs by itself: scheduled jobs (`Server.Schedule`)
run once per period and write one result, continuous jobs
(`Server.Continuous`) follow their files and keep writing results. This page
holds the details; the short version is in the
[Server Configuration Guide](./server-configuration.md).

## Example

```json
"Schedule": [
  {
    "Name": "daily-errors",
    "Enable": true,
    "Files": "/var/log/app/app-$today.log",
    "Query": "select count(*)",
    "Outfile": "out/daily-errors-$today.map",
    "Servers": ["localhost"],
    "AllowFrom": ["localhost"],
    "TimeRange": [9, 17]
  }
],
"Continuous": [
  {
    "Name": "live-errors",
    "Enable": true,
    "Files": "/var/log/app/errors.log",
    "Query": "select count(*)",
    "Outfile": "out/live-errors.map",
    "AllowFrom": ["localhost"],
    "RestartOnDayChange": true
  }
]
```

## Job fields

| Field | Applies to | Meaning |
| --- | --- | --- |
| `Name` | both | Job name, also the job's password (below). |
| `Enable` | both | `true` to run the job (default `false`). |
| `Files` | both | Comma-separated files or globs to read. Date placeholders allowed. |
| `Query` | both | A [query language](./querylanguage.md) MapReduce query without an `outfile` clause; the scheduler appends `outfile <Outfile>`. |
| `Outfile` | both | Result file (CSV). Date placeholders allowed. |
| `Servers` | both | Servers to query. Empty: this `dserver`. |
| `Discovery` | both | Optional: how `Servers` is read, `COMMA` or `FILE`. |
| `AllowFrom` | both | Addresses the job's connections are accepted from. Required in practice. |
| `TimeRange` | `Schedule` | `[startHour, endHour]`; the job runs while `startHour <= hour < endHour`. |
| `RestartOnDayChange` | `Continuous` | Restart the job at midnight with the new dates. |

Both runners start 2 s after `dserver` started. The scheduler then checks its
jobs every minute.

### Date placeholders

`Files` and `Outfile` take these placeholders, replaced by a date as
`YYYYMMDD`:

| Placeholder | Date |
| --- | --- |
| `$today` | today |
| `$tomorrow` | tomorrow |
| `$yesterday` | 1 day ago |
| `$yyesterday` | 2 days ago |
| `$yyyesterday` | 3 days ago |

These are calendar days in the scheduling server's local timezone, also
across daylight-saving changes. Both fields use the same time, captured once
per run.

### Outfile

The result is written to a `.tmp` file and renamed, together with a
`<Outfile>.query` file holding the query.

A scheduled job does not run while its outfile exists. An `Outfile` without
a date placeholder therefore makes the job run once, until the file is
removed.

### TimeRange

`[0, 24]` is the whole day. A job without `TimeRange` (`[0, 0]`) never runs.

### Servers and Discovery

`Servers` is joined with commas into the client's `--servers` value.
Whitespace around entries and empty entries are ignored.

| `Discovery` | `Servers` | Servers queried |
| --- | --- | --- |
| *(none)* | list of servers | Those servers. |
| *(none)* | one entry naming an existing file | The servers listed in that file. |
| *(none)* or `COMMA` | empty or only blank entries | This `dserver` itself. |
| `COMMA` | list of servers | Those servers. |
| `FILE` | one server list file | The servers listed in that file. |
| `FILE` | empty | None: the job fails (`FILE` looks for a file named after this `dserver`'s address). |

* A server list file has one server per line. Blank lines, `#` comment lines
  and a leading UTF-8 byte order mark are ignored.
* "This `dserver`" is its `Server.SSHBindAddress` at `Common.SSHPort`. When
  it listens on every address (`""`, `0.0.0.0`, `::ffff:0.0.0.0` or `::`)
  the job dials the loopback address: `127.0.0.1`, or `::1` for `::`.
* `Discovery` is case-insensitive and is the client's `--discovery` value.
  Builds may add more modules; only those take a `MODULE:options` suffix.
* `COMMA` and `FILE` take no options. `comma:server1,server2` or
  `file:/path/to/serverlist` fails the job's client with `discovery module
  "comma" takes no options; put the servers in Servers/--servers`. A
  scheduled job then writes no outfile and retries after its backoff, a
  continuous job logs the error and retries a minute later. Put the servers,
  or the server list file, in `Servers`:

```json
"Discovery": "file",
"Servers": ["/etc/dserver/serverlist"]
```

### Authentication: Name and AllowFrom

A job connects as user `DTAIL-SCHEDULE` (scheduled) or `DTAIL-CONTINUOUS`
(continuous) with its `Name` as password. The name is a shared secret: keep
it unguessable and the configuration file unreadable for others.

A `dserver` accepts a job connection only when:

* its own configuration has a job of the same type and `Name` (enabled or
  not), and
* that job's `AllowFrom` contains the connecting address (hostnames are
  resolved). An empty `AllowFrom` accepts none.

So every `dserver` named in `Servers` needs the job entry, with the
scheduling `dserver`'s address in `AllowFrom`.

A job with empty `Servers` (or `localhost`) on a `dserver` listening on every
address connects from the loopback address. Its `AllowFrom` must resolve to
`127.0.0.1` or `::1`, e.g. `localhost`.

The [read permissions](./read-permissions.md) do not apply to job
connections. A `journal:` target in `Files` must still name one literal unit
with its type (`journal:nginx.service`); `journal:nginx`,
`journal:*.service` or `journal:system.slice`, accepted by older releases,
fail every run.

## Scheduled jobs: failures and retries

A scheduled job writes its outfile only when its run completed. There are no
interim results.

### Within the TimeRange

Any failure blocks the outfile. The job:

* writes nothing and leaves an outfile of an earlier run untouched,
* logs `Job <name> failed and wrote no outfile <path> (failure <n> in a
  row), it runs again from about <time>`,
* runs again after a backoff of 1, 2, 4, 8, 16, 32 and then every 60
  minutes, counted from the start of the failed run, but at least half that
  time after the run ended.

### Two kinds of failure

| Kind | Examples | Client log line |
| --- | --- | --- |
| File read failure | `read: no file to read`, `read: no permission to read file`, `read: unable to create file reader`, `read: unable to read file`, `read: more files than the server reads` | `Not writing the mapreduce result as files could not be read ...` |
| Transport failure | No connection, failed SSH handshake, rejected session, session cut short without the server's close handshake, a server without journal support for a `journal:` target, or another failed command such as `map: invalid query` | `Not writing the mapreduce result as the query did not complete` |

File read failures cover a file that does not exist yet (e.g. `$today`'s
log), a `Files` entry that never appears, a glob matching no file and a file
missing on one of several servers.

### After the TimeRange ended: final runs

Once the `TimeRange` of a failed run has ended (its end hour on the day of
the run; for `[0, 24]` the next midnight), the scheduler goes on with *final
runs* for the same files and outfile:

| Failures of the final run | Result |
| --- | --- |
| None | The complete outfile. |
| Only file read failures | The outfile with what could be read; the client logs `Writing partial mapreduce result after the job's TimeRange ended`. |
| A transport failure | No outfile; another final run after the backoff. |

* The first final run starts at the first scheduler run after the range
  ended, the later ones after the backoff, which goes on counting.
* A final run logs `Starting final run of job <name> for outfile <path>
  after its TimeRange ended at <time>, ...`.
* 24 hours after the `TimeRange` ended the scheduler gives up: `Giving up
  job <name> after <n> failures in a row: it wrote no outfile <path> within
  24h0m0s ...`.
* For an outfile without dates the next day's `TimeRange` takes over: runs
  in it are strict again and the failure count starts over.

### After a restart

Failure state is kept in memory only. After a `dserver` restart within the
`TimeRange` the job runs strictly again. A `dserver` restarted after the
range ended does not know the failed run and writes no outfile for that
period.

### Jobs that run together

Due jobs that read the same files from the same servers, all of them this
`dserver`, run together and share one read of each file (see "Shared reads"
in the [Server Configuration Guide](./server-configuration.md)). A group
runs in waves of at most `min(MaxConnections / 4, MaxConcurrentCats)`
divided by the number of servers, and of at least one job. Other jobs, and
all jobs with `Server.SharedReadsDisable`, run one at a time.

## Continuous jobs

A continuous job follows its files from their current end, like `dtail`, and
writes the result of each query `interval` to its outfile.

* It writes interim and final results whatever its exit status, as
  interactive `dmap` with an outfile does.
* A job that exits is started again a minute later, without a backoff.
* With `RestartOnDayChange` it is restarted at once at midnight, with the
  new dates filled in.

## A job without any server

A job whose servers name no server at all, e.g. a server list file of only
blank and comment lines, fails before it connects: `no server to connect
to`, logged at `WARN` as `Unable to create job <name>`.

* A scheduled job counts that as a failed run (backoff as above).
* A continuous job writes no outfile, so an earlier outfile is not
  overwritten with empty results, and runs again a minute later.
