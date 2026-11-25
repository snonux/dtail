# Interactive mode

In interactive mode a client keeps its SSH sessions open and takes commands
on the terminal, so a running filter or query is changed without reconnecting.
This guide covers the essentials; every detail is in the
[reference](./interactive-query-reference.md).

![dtail-interactive](dtail-interactive.gif "Interactive dtail: reload, query and tail")

## When it is on

| Client | Interactive |
| --- | --- |
| `dtail` | By default on a terminal. `--no-interactive` turns it off. |
| `dmap` without a query | On a terminal it opens the [query builder](#the-dmap-query-builder). |
| `dcat`, `dgrep`, `dmap` with a query | Only with `--interactive-query`; the session then stays open after the workload finished. |

Pipes, redirections, cron and scripts are never interactive unless
`--interactive-query` is given. Details:
[reference, "Default"](./interactive-query-reference.md#default-on-for-dtail-in-a-terminal).

## Commands

| Command | What it does |
| --- | --- |
| `:reload <flags>` | Change the workload with the client's flags, e.g. `:reload --grep WARN`. |
| `:query <query>` | `dtail`, `dmap`: run a MapReduce query on the session, typed without shell quoting. `:query` alone shows the running one. |
| `:tail [regex]` | `dtail`: go back from a query to following lines, optionally with a new regex. |
| `:show` | Print the current workload, files, regex or query, servers and sessions. |
| `:fields [table]` | Print the tables, fields and sample values the servers found in the files. |
| `:probe` | Ask the servers for those fields now. |
| `:help` | Print the commands with examples for the client's mode. |
| `:quit` | End the session. |

A command that is rejected (unknown flag, a query that does not parse) leaves
the running workload unchanged. Details:
[reference, "Control commands"](./interactive-query-reference.md#control-commands).

## Keys

* **Enter** on an empty line pauses the output, and resumes it again. Typing
  pauses it too, until the line is entered.
* **Tab** completes commands, flags, remote file paths inside `--files`, and
  queries: it guides through `from`, the table, `select`, `where`, `group by`,
  `order by` and `limit` with the tables and fields of your logs.
* **Up/Down** recall earlier lines. **Alt-E** opens the line in `$EDITOR`.
* `--edit-mode vi` adds vi keys.

Details: [line editing and completion](./interactive-query-reference.md#line-editing-and-completion),
[pausing](./interactive-query-reference.md#pausing-the-output),
[colors](./interactive-query-reference.md#colors).

## `:reload` flags

| Client | Flags |
| --- | --- |
| all | `--files`, `--plain`, `--quiet`, `--timeout` |
| `dtail` following lines, `dgrep` | also `--grep`/`--regex`, `--before`, `--after`, `--max`, `--invert` |
| `dmap`, `dtail` running a query | also `--query` |

Flags not given keep their value. Values are quoted as in a shell, without
expansion: `:reload --regex '\d+ ms'`. `--regex ''` clears the regex. Details:
[reload flags](./interactive-query-reference.md#reload-flags),
[quoting](./interactive-query-reference.md#quoting).

## Examples

Change the filter of a running `dtail`, switch it to a query and back:

```
dtail --servers app01 --files /var/log/app.log --grep ERROR
:reload --grep WARN
:query from STATS select count($line) group by $hostname
:tail
```

Replace the query of a `dmap` that stays open:

```
dmap --servers app01 --files /var/log/app.log --interactive-query \
  --query 'from STATS select count($line) group by $hostname'
:query from STATS select count($line),avg(latency) group by $hostname
```

More, including a query built step by step with Tab:
[reference, "Examples"](./interactive-query-reference.md#examples).

## The `dmap` query builder

![dmap-query-builder](dmap-query-builder.gif "The dmap query builder")

`dmap` started without a query on a terminal opens a builder instead of
failing:

```
dmap --servers app01 --files /var/log/app.log
query>
```

The line you type is the query. Tab guides through it, `:fields` lists what
the files hold, Enter runs the query and `dmap` exits with the result as
`dmap --query` does. `:quit` leaves without running. `--no-interactive` keeps
the old error. Details:
[reference, "The dmap query builder"](./interactive-query-reference.md#the-dmap-query-builder).

## See also

* [Reference](./interactive-query-reference.md): all rules, limits and how it
  works (sessions, acknowledgements, compatibility).
* [Control channel for programs and agents](./agent-control.md): the same
  features as JSON requests, without a terminal.
* [DTail Query Language](./querylanguage.md) for the query syntax.
