# Shared reads: sharing on vs off benchmark (2026-09)

Status: **no timing results yet.** The benchmark script exists and was
checked to work, but this machine was never quiet while it was written
(see "Conditions"), so no CPU or elapsed time numbers are recorded here.
Run it on a quiet machine and fill in the tables below.

## What is measured

`benchmarks/shared_read_bench.sh` starts a dserver twice per run, once with
shared reads on (the default) and once with `"SharedReadsDisable": true`,
interleaving the two modes and alternating which goes first. Scenarios:

| Scenario | Workload |
|---|---|
| `follow` (CSV: `follow-burst`) | N `dtail --plain` clients follow one empty file; 100 MiB (818,561 lines, the `normal` generator of `upstream_vs_local_bench.sh`) are appended in one `cat`. Each client matches about 0.1% of the lines. |
| `follow-paced` | The same, appended in 1 MiB writes every 100 ms (about 10 MiB/s). |
| `scheduled` | N scheduled MapReduce jobs (four different `from STATS` queries) on one 100 MiB `stats` file. With sharing on they run as one group and dserver reads the file once; with sharing off one at a time with private reads. |
| `scheduled-gz` | The same on the gzip-compressed file. |

The input files are 100 MiB by default; `-b BYTES` generates them with
another size instead (for example `-b 1048576` for a quick check). They are
kept in the work directory under names holding their size.

Follow sessions on compressed files always read privately, so there is no
`.gz` follow scenario: with sharing on, two `dtail` sessions on a `.gz` file
logged `Start reading` but no shared read line (checked 2026-09-22).

Per run the script records:

- dserver CPU: `utime + stime` from `/proc/<pid>/stat`, from just before the
  data is appended (follow) or after the server is up (scheduled), to when
  every client printed the end marker or every job logged its exit;
- elapsed time over the same window (scheduled: from the first
  `Starting job` log line);
- with `-s`, in separate runs with dserver under `strace -f -y`: read
  syscalls on the input file, and all read syscalls (the latter include
  socket reads). CPU and time are not recorded in these runs because of the
  ptrace overhead;
- shared read log lines (`Shared ... read started`) and evictions;
- the 1-minute load average at the start;
- `output_ok`: every follow client printed exactly the matching lines of
  the input (`grep -E 'user999 |BENCH'`), every scheduled job exited with
  status 0 and wrote the same outfile as the first run of the scenario with
  the same input size, number of jobs and dserver build
  (`runs/scheduled-<plain|gz>-<bytes>-n<N>-<dserver sha256 prefix>-reference`
  in the work directory, taken from a run whose jobs all exited with status 0;
  a rebuilt dserver takes a new reference). The reference comes from whichever
  mode ran first, so a scheduled row shows `output_ok=yes` for both modes only
  when sharing on and off wrote identical outfiles.

The clients use their own `known_hosts` file in the work directory, which
pins the benchmark's host key, and get no stdin, so a wrong server makes them
fail instead of prompting. dserver listens on the first free port from 24900
on, or on the one given with `-p`; a run counts as started only once this
run's dserver listens on it (checked with `ss`).

## How to run

```bash
make build
# CPU and elapsed time, 3 runs per mode, waiting up to 10 min per run for
# a quiet machine (load1 < 1.0, no other dserver, make or go test):
benchmarks/shared_read_bench.sh -n 4 -r 3 -q 600 -o results.csv follow
benchmarks/shared_read_bench.sh -n 4 -r 3 -q 600 -o results.csv follow-paced
benchmarks/shared_read_bench.sh -n 4 -r 3 -q 600 -o results.csv scheduled
benchmarks/shared_read_bench.sh -n 4 -r 3 -q 600 -o results.csv scheduled-gz
# read syscalls:
benchmarks/shared_read_bench.sh -n 4 -r 1 -s -o results.csv scheduled
# quick check that the script works, with 1 MiB inputs:
benchmarks/shared_read_bench.sh -n 2 -r 1 -b 1048576 scheduled
```

## Conditions (2026-09-22)

Host: Rocky 9 bhyve guest, 4 vCPUs, the dev host of
`performance-plan-2026-09-15.md`. Two other agents ran test suites and
stress scripts in parallel the whole time. The script's quiet check
(1-minute load below 1.0 and no other dserver, make or go test process)
was polled every 60 s for about 85 minutes (03:37-03:53 and 09:19-10:25)
and never passed: at every check the 1-minute load was at least 1.0 (up
to 15) or another `go test`, `make` or `dserver` process was running,
mostly both.

## Functional check on the loaded machine (not results)

Single smoke runs with N=4 (one follow burst run with N=2), load average
2.2-4.5 at the start of each run, were used only to check that the script
works. The CPU and elapsed times
of these runs are not reported, as the load of the other agents distorts
them. Two observations from them do not depend on timing, or show
behaviour rather than cost:

- Every run had `output_ok=yes`: with sharing on, the four follow clients
  printed the same lines as with sharing off, and the four scheduled jobs
  wrote the same outfiles.
- Read syscalls on the input file, one run each (strace):

  | Scenario | Sharing on | Sharing off |
  |---|---:|---:|
  | `scheduled` (plain) | 102 | 408 |
  | `follow` (burst) | 6,200 | 6,430 |

  The group read reads the plain file once instead of four times. In the
  follow burst run all four sessions were evicted from the shared reader
  (four `evicted a slow subscriber` lines) and went on with private
  readers, so the reads were nearly as many as without sharing. In the two
  burst runs without strace (N=2 and N=4) every session was evicted too.
  This matches the known limitation in `AGENTS.md` (a large burst can evict sessions that are only
  slower than the reader; they do not rejoin, task c9). How often this
  happens on a quiet machine is still to be measured; in the one paced run
  no session was evicted.

## Results

To be filled in from a run on a quiet machine.

| Scenario | Mode | Runs | Elapsed (s) | dserver CPU (s) | File reads | Evictions |
|---|---|---:|---:|---:|---:|---:|
| follow | on | | | | | |
| follow | off | | | | | |
| follow-paced | on | | | | | |
| follow-paced | off | | | | | |
| scheduled | on | | | | | |
| scheduled | off | | | | | |
| scheduled-gz | on | | | | | |
| scheduled-gz | off | | | | | |
