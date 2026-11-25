# Agent control channel (`--control json`)

The way for an agent to work with logs over several steps: one process, one
set of SSH connections, JSON in and out. Use it instead of the terminal-only
interactive mode. Full reference: `doc/agent-control.md` in the repository.

## When to use it

- You do not know the tables and fields of the logs yet: ask with `fields`
  before writing a query.
- You will run more than one query, or refine one.
- You want results as data (`columns`, `rows`), not a printed table.

For a single known query, plain `dmap --query '...' --no-interactive` is
still fine.

## Start

```sh
dir=$(mktemp -d); mkfifo "$dir/in"
dmap --servers servers.txt --files '/var/log/app/*.log' --trustAllHosts \
     --control json --control-in "$dir/in" --control-out "$dir/out" \
     > "$dir/data" 2> "$dir/err" &
```

- `dmap` without `--query`: connections are held; each `query` request runs
  over the whole files and returns the **final** result.
- `dtail` instead of `dmap`: follows new lines to stdout; `query` aggregates
  the lines arriving from then on (results are `result` events), `tail`
  goes back.
- Diagnostics go to the client log file (`~/log/YYYYMMDD.log`), not to
  stdout or the control output. Keep it that way: do not add
  `--logger stdout`. Read the log file only when something fails.
- No `--servers` means local serverless mode, as always.

## Send a request, read its response

```sh
req() {  # req ID 'JSON'  -> prints the response line of that id
    echo "$2" > "$dir/in"
    until grep -m1 "^{\"id\":$1," "$dir/out"; do sleep 0.3; done
}
req 1 '{"id":1,"cmd":"fields"}'
req 2 '{"id":2,"cmd":"query","query":"from STATS select $hostname,count($line) group by $hostname"}'
req 3 '{"id":3,"cmd":"quit"}'
```

Use a new numeric `id` per request. Every request gets exactly one line
with `"ok":true` and a `result`, or `"ok":false` and an `error`. Lines with
`"event"` are not responses: `ready` (first line), `progress` (while a query
runs, with the request's id), `result` (query results of a `dtail` session).

For a long query, show the user the last `progress` event about every 10
seconds while you wait: `grep '"event":"progress"' "$dir/out" | tail -n 1`
(`percent`, `bytes`, `total`, `eta`).

## Commands

| `cmd` | Fields | Use |
| --- | --- | --- |
| `fields` | `table` (optional) | Tables, fields, sample values, `$variables`. Do this first. With `table` the table is searched for in a larger part of the files. |
| `list` | `prefix` | Paths on the server (`journal:` lists units). |
| `complete` | `query` | What can follow a partial query: `candidates`, `heading`. |
| `query` | `query` | Run a query. `dmap`: the answer is the final result. |
| `reload` | `args` | Change files or flags: `{"cmd":"reload","args":["--files","/var/log/b.log"]}`. One array element per argument, no quoting. |
| `tail` | `regex` (optional) | `dtail` only: follow lines again. |
| `show` | | What the session runs, servers, sessions. |
| `probe`, `pause`, `resume`, `help`, `quit` | | Probe now; stop/release output; help; end. |

## Reading a query result

```json
{"id":2,"ok":true,"result":{"columns":["$hostname","count($line)"],"rows":[["web01","120"]],
 "rowsTotal":1,"final":true,"status":0,"warnings":["web02: read: no file to read"],
 "read":{"bytes":1048576,"total":1048576,"files":2,"filesTotal":2,"reporting":2,"servers":2}}}
```

- Values are strings.
- `warnings` names files that could not be read and servers whose session
  ended early. Report them: a result with warnings is not a fleet-wide
  answer.
- `read.reporting` below `read.servers` means some servers did not report
  how much they read.
- Without a `limit` clause at most 200 rows are returned; `truncated` and
  `rowsTotal` tell when there are more. Add `limit N` or an `outfile`.
- Queries run one after the other; a running query blocks the next request.

## Caveats

- An unknown host is refused (nobody can answer the question): use
  `--trustAllHosts` if the user accepts that, or `known_hosts`.
- `fields` shows a sample of the files (their last part): a field missing
  there can still exist. A table named in `fields` with `table` is searched
  for more widely.
- End the session with `quit`; closing a FIFO writer does not end it.
