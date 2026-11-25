# dserver scheduled and continuous jobs

`dserver` runs MapReduce queries on its own, as a client of itself or of
other dservers, and writes CSV outfiles. Jobs are arrays in the `Server`
section.

```json
"Schedule": [{
  "Name": "daily-errors", "Enable": true,
  "Files": "/var/log/app/app-$today.log",
  "Query": "from REQUESTS select status,count(*) group by status",
  "Outfile": "out/daily-errors-$today.csv",
  "Servers": ["web01:2222", "web02:2222"],
  "AllowFrom": ["sched01.example.org"],
  "TimeRange": [9, 17]
}],
"Continuous": [{
  "Name": "live-errors", "Enable": true,
  "Files": "/var/log/app/errors.log",
  "Query": "select $hostname,count(*) group by $hostname interval 60",
  "Outfile": "out/live-errors.csv",
  "AllowFrom": ["localhost"],
  "RestartOnDayChange": true
}]
```

## Fields

| Field | Meaning |
| --- | --- |
| `Name` | Job name. Its connections log in as user `DTAIL-SCHEDULE` or `DTAIL-CONTINUOUS` with the name as password (a shared secret). |
| `Enable` | `true` to run. |
| `Files` | Comma list or glob (or `journal:UNIT.TYPE`); date placeholders allowed. |
| `Query` | MapReduce query ([query-language.md](query-language.md)) without `outfile` (the scheduler adds it); not checked at startup. |
| `Outfile` | Result CSV (plus `.query`); date placeholders allowed. Without a placeholder a scheduled job runs once, as it skips while the outfile exists. |
| `Servers` | Server list; one existing file path = server list file; empty = this dserver (loopback when bound to all addresses). |
| `Discovery` | Optional `COMMA` or `FILE` (no `:options`). |
| `AllowFrom` | **Required in practice.** Every target dserver accepts a job connection only if its own config has a job of the same type and `Name` (enabled or not) whose `AllowFrom` holds the caller's address; empty accepts nobody. Use `localhost` for an empty `Servers`. |
| `TimeRange` | Schedule only: `[startHour, endHour]`, runs while `start <= hour < end`; `[0, 24]` all day. Empty = never runs. |
| `RestartOnDayChange` | Continuous only: restart when date placeholders roll over. |

Date placeholders (local time, `YYYYMMDD`): `$today`, `$tomorrow`,
`$yesterday`, `$yyesterday` (2 days ago), `$yyyesterday` (3 days ago).

## How scheduled jobs run

- Checked every minute (first 2 s after start); a job is skipped for its
  date period once its outfile exists. Outfile and `.query` file are written via `.tmp` and
  rename, final result only.
- Due jobs reading the same files of this dserver are grouped in waves
  and share one read (unless `Server.SharedReadsDisable`).
- Jobs use their own auth, no `known_hosts`, and bypass `Server.Permissions`.

## Failure semantics

- **Within the TimeRange, any failure writes no outfile** (an older one
  stays) and the job retries after 1, 2, 4, 8, 16, 32, then every 60
  minutes: `Job <name> failed and wrote no outfile <path> (failure <n> in a
  row), it runs again from about <time>`.
- **Transport failures** (no connection, failed handshake, session cut
  short, cancelled, missing `journal-v1`, `map: invalid query`, `map:
  incomplete result`, output overflow, unknown reasons) never write an
  outfile.
- **File read failures** (`read: no file to read`, `no permission`,
  `unable to read file`, `unable to create file reader`, `more files than
  the server reads`): after the TimeRange ends, **final runs** write the
  partial result (`Writing partial mapreduce result after the job's
  TimeRange ended|...`), or the full one if all files are readable by then.
- 24 h after the TimeRange ended the scheduler gives up (`Giving up job
  ...`). A dserver restart forgets all failure state.
- A job whose discovery yields no server fails at creation (`no server to
  connect to`, WARN `Unable to create job <name>`).

## Continuous jobs

Follow their files from the end, non-cumulative: every query interval
writes that interval's rows (to append, set `"Outfile": "append
path.csv"`, as the scheduler builds `<Query> outfile <Outfile>`). `RestartOnDayChange`
restarts at midnight so date placeholders roll over. They write their
results whatever the exit status and restart a minute after exiting,
without backoff. A job that cannot even be created (bad query, no server)
writes no outfile and logs `Unable to create job <name>` every minute.

## Checking jobs

- `grep 'Unable to create job\|failed and wrote no outfile\|Giving up job' log/*.log`
- Test a job query first: `dmap --servers ... --files ... --query '...'`.
