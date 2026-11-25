# Control channel for programs and agents

`--control json` gives a program what the [interactive mode](./interactive-query.md)
gives a person: a session that stays open and takes commands. The program
sends one JSON request per line and gets exactly one JSON line back for each.
No terminal is needed, and the SSH connections stay open between requests.

It is made for LLM agents: discover the fields of the logs, run a query, read
the result as data, refine the query, all on one process.

## Start a session

```sh
mkfifo ctl.in
dmap --servers servers.txt --files '/var/log/app/*.log' --trustAllHosts \
     --control json --control-in ctl.in --control-out ctl.out > data.txt &
```

| Flag | Meaning |
| --- | --- |
| `--control json` | Turn the control channel on. Implies `--interactive-query`. |
| `--control-in PATH` | Read the requests from this file or FIFO. Default: stdin. A FIFO stays open across its writers, so `echo '{...}' > ctl.in` works for every request. |
| `--control-out PATH` | Append the responses and events to this file. Default: stderr. |

Stdout carries only data (followed lines). Diagnostics go to the log file
(`~/log/YYYYMMDD.log`, logger `fdata`) and not to stdout, unless `--logger`
names another logger. A host key question is answered "no": use
`--trustAllHosts` or a `known_hosts` entry.

| Client | Session |
| --- | --- |
| `dmap` without `--query` | Holds its connections without a workload. Every `query` request runs the query over the whole files and is answered with the **final** result. |
| `dtail` | Follows lines to stdout. `query` switches it to a query over the lines arriving from then on; results come as `result` events. `tail` switches back. |
| `dmap --query`, `dcat`, `dgrep` | The interactive session of that client, with `reload` to change it. |

## Requests and responses

```json
{"id": 1, "cmd": "fields", "table": "STATS"}
{"id": 1, "ok": true, "result": {...}}
{"id": 2, "ok": false, "error": "invalid query: ..."}
```

`id` is any JSON value and comes back unchanged. Requests are handled one
after the other. A request with a field no command knows is rejected, so a
typo does not run something else. Lines without `ok` are events:

| Event | When |
| --- | --- |
| `ready` | First line: the session takes requests. Lists the `commands` it has. |
| `progress` | While a `dmap` query request runs: `bytes`, `total`, `percent`, `eta`, with the request's `id`. |
| `result` | A result of the query a `dtail` (or interactive `dmap`) session runs: `columns`, `rows`, `final`. |

## Commands

| `cmd` | Fields | Result |
| --- | --- | --- |
| `fields` | `table` (optional) | Tables, fields, sample values and `$variables` found in the files. With `table`, that table is probed on its own. |
| `list` | `prefix` | Files and directories on the server starting with `prefix` (`journal:` lists units). |
| `complete` | `query`, or `line` and `cursor` | What Tab would do: the completed text and the candidates that can follow. |
| `query` | `query` | `dmap`: the final result (`columns`, `rows`, `rowsTotal`, `warnings`, `read`). `dtail`: the query applied; results follow as events. |
| `tail` | `regex` (optional) | `dtail`: back to following lines. |
| `reload` | `args` | Change the workload with the client's flags, one argument each: `["--files","/var/log/b.log"]`. |
| `show` | | Workload (`builder`: `dmap` holding no workload), files, query, regex, servers, sessions, paused. |
| `probe` | | Probe the servers now; which answered. |
| `pause`, `resume` | | Stop and release the output. |
| `help` | | The commands and the interactive help text. |
| `quit` | | End the session. The end of the input does the same. |

A result without a `limit` clause carries at most 200 rows; `rowsTotal` and
`truncated` tell when there are more. A query with an `outfile` clause writes
the file and returns its path instead of rows.

## Example

```sh
echo '{"id":1,"cmd":"fields"}' > ctl.in
echo '{"id":2,"cmd":"query","query":"from STATS select $hostname,max($goroutines) group by $hostname"}' > ctl.in
grep '"id":2' ctl.out      # the response, once the query ended
echo '{"id":3,"cmd":"quit"}' > ctl.in
```

```json
{"id":2,"ok":true,"result":{"query":"from STATS select ...","columns":["$hostname","max($goroutines)"],
 "rows":[["web01","41"],["web02","38"]],"rowsTotal":2,"final":true,"status":0,
 "read":{"bytes":1288490188,"total":1288490188,"files":4,"filesTotal":4,"reporting":2,"servers":2}}}
```

Check `warnings` in the result before calling it complete: it names files
that could not be read and servers whose session ended early.

A one-shot use needs no FIFO:

```sh
echo '{"id":1,"cmd":"fields"}' | dmap --files app.log --control json 2>&1 >/dev/null
```

## See also

* [Interactive mode](./interactive-query.md) for the same features on a terminal.
* [DTail Query Language](./querylanguage.md) for the query syntax.
* [Read progress](./read-progress.md) for what `bytes` and `total` count.
