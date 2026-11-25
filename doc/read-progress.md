Read progress of `dcat`, `dgrep` and `dmap`
==========================================

Reading a lot of logs takes a while. Every `dserver` reports how many bytes of
its files it has read so far, and the client sums that up over all servers.
The serverless mode reports the same way.

## Two ways to see it

| | `STATS` lines (default) | `PROGRESS` lines (`--progress`) |
|---|---|---|
| Where | the client's log output, at log level `info` | stderr |
| How often | every 3 seconds, while something changed | every second, while the progress changed |
| Field order | sorted by name | fixed, the bar first |
| `--quiet`, `--plain` | drop the lines | no effect |
| At the end | nothing more | one final line |

A `STATS` line carries the progress among the connection statistics:

```console
CLIENT|workstation01|INFO|STATS:STATS|bar=[######--------------]|bytes=1288490188|cgocalls=0|connected=2|connected%=100|cpu=8|eta=35s|files=3/10|goroutines=31|new=0|progress=1.20GiB/4.00GiB|progress%=30.0|rate=85.30MiB/s|reporting=2/2|servers=2|throttle=0|total=4294967296
```

The serverless mode logs at level `warn` by default, so add `--logLevel info`
to see `STATS` lines there. The statistics printed on Ctrl-C carry the
progress fields too.

## `--progress`

Use `--progress` when `--plain` output goes to a file or another program.
Stdout keeps the data only:

```shell
% dgrep --servers serverlist.txt --files '/var/log/app/*.log.gz' --regex ERROR \
    --plain --progress > errors.txt
PROGRESS|bar=[######--------------]|progress%=30.0|progress=1.20GiB/4.00GiB|rate=85.30MiB/s|eta=35s|files=3/10|reporting=2/2|bytes=1288490188|total=4294967296
...
PROGRESS|bar=[####################]|progress%=100.0|progress=4.00GiB/4.00GiB|rate=86.10MiB/s|eta=0s|files=10/10|reporting=2/2|bytes=4294967296|total=4294967296
```

* The client writes one final line when it is done. If the last line already
  showed the final numbers, that line is the final one and is not repeated.
* Interactive sessions (`--interactive-query`, interactive `dtail`, the `dmap`
  query builder) write no `PROGRESS` lines.

## The fields

| Field | Meaning |
|---|---|
| `bar` | The percentage as 20 cells, one `#` per 5% read. All `?` while no server reported. |
| `progress%` | The percentage read. `unknown` while no server reported. |
| `progress` | Bytes read and total, in IEC units. |
| `rate` | Measured over the last 15 seconds. `unknown` until a rate is known. |
| `eta` | The time left at that rate. `unknown` until a rate is known. |
| `files` | Files whose read ended, of the files to read. |
| `reporting` | Servers that reported progress, of all servers. |
| `bytes`, `total` | The raw byte counts. |

## What is counted

* **Compressed files** count their compressed size on disk.
* **Growing files.** A log still written to raises the total while it is
  read, as the read goes on to its end.
* **Ended reads.** Once the read of a file ended, the file counts with the
  bytes read of it.
* **Early ends.** When `--max` or an error ended a read early, the rest of the
  file leaves the total. The final line says 100%, and `bytes` is what was
  really read. A `--max 1` grep of a 1 GB file ends at about 1 MiB of 1 MiB.
* **Failures** are reported as before, by the exit status and `WARN` lines.
  See [exit-statuses.md](./exit-statuses.md).

## When numbers look odd

* **The percentage falls.** Every entry of `--files` is registered when its
  read command starts. With several entries the total can still grow in the
  first moments.
* **`reporting` is below the number of servers.** The total then lacks the
  files of the other servers.
* **`reporting=0/N` on the final line.** No server reported. A `dserver` older
  than this feature reports nothing. Neither does one that read no file,
  because none matched or none was permitted.

## What reports no progress

* `journal:` units and stdin, which have no size.
* `dtail` following files, which has no end.

The wire frame and the implementation are described in
[../docs/dev/read_progress.md](../docs/dev/read_progress.md).
