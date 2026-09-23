# Shared reads: sharing on vs off benchmark (2026-09)

[The 2026-09-23 follow-up](performance-followup-2026-09-23.md) reruns this
exact baseline alongside the pre-plan and final builds. It reports the
remaining paced-follow CPU gap as well as sparse, idle and file-sink results.

Results of `benchmarks/shared_read_bench.sh` with N=4 sessions or jobs and
100 MiB inputs, on master at 1a1fc0f (d9 merged, rejoin from c9 and the
rotation drain from f9 included), run on 2026-09-22.

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
# CPU and elapsed time, 3 runs per mode, waiting up to 15 min per run for
# a quiet machine (load1 < 1.0, no other dserver, make or go test):
benchmarks/shared_read_bench.sh -n 4 -r 3 -q 900 -o results.csv follow
benchmarks/shared_read_bench.sh -n 4 -r 3 -q 900 -o results.csv follow-paced
benchmarks/shared_read_bench.sh -n 4 -r 3 -q 900 -o results.csv scheduled
benchmarks/shared_read_bench.sh -n 4 -r 3 -q 900 -o results.csv scheduled-gz
# read syscalls, one run per scenario:
benchmarks/shared_read_bench.sh -n 4 -r 1 -s -o results.csv scheduled
benchmarks/shared_read_bench.sh -n 4 -r 1 -s -o results.csv scheduled-gz
benchmarks/shared_read_bench.sh -n 4 -r 1 -s -o results.csv follow
# quick check that the script works, with 1 MiB inputs:
benchmarks/shared_read_bench.sh -n 2 -r 1 -b 1048576 scheduled
```

## Conditions (2026-09-22, 15:30-16:15)

Host: Rocky 9 bhyve guest, 4 vCPUs, the dev host of
`performance-plan-2026-09-15.md`. No other agent, test suite or dserver ran.
Every run waited for the script's quiet check (1-minute load below 1.0, no
other dserver, make or go test process), except the strace runs (`-s`, no
`-q`), which started at a load of 0.56-1.94. The first `follow` series
stopped before its third `off` run because the machine did not become quiet
within 900 s; one more `follow` pair was run separately, and both its runs are
included below (its `off` run is the 8.49 s outlier).

## Results

Medians over the runs; every run had `output_ok=yes`: with sharing on the
follow clients printed the same lines and the scheduled jobs wrote the same
outfiles as with sharing off. File reads are read syscalls on the input file
from one strace run per mode.

| Scenario | Mode | Runs | Elapsed (s) | dserver CPU (s) | File reads | Evictions |
|---|---|---:|---:|---:|---:|---:|
| follow (burst) | on | 4 | 4.84 | 17.01 | 6,210 | 4 per run |
| follow (burst) | off | 3 | 4.76 | 16.93 | 6,435 | - |
| follow-paced | on | 3 | 11.32 | 28.99 | | 0 |
| follow-paced | off | 3 | 12.27 | 25.50 | | - |
| scheduled | on | 3 | 0.89 | 3.44 | 102 | 0 |
| scheduled | off | 3 | 3.29 | 3.24 | 408 | - |
| scheduled-gz | on | 3 | 0.94 | 3.76 | 623 | 0 |
| scheduled-gz | off | 3 | 3.49 | 3.48 | 2,492 | - |

Ranges: follow on 4.75-4.89 s / 16.96-17.25 s CPU, off 4.69-8.49 s (one
outlier; the others 4.69 and 4.76) / 16.84-18.64 s CPU; follow-paced on
11.29-11.48 s / 28.55-29.33 s, off 12.17-12.28 s / 25.24-25.55 s;
scheduled on 0.88-0.95 s / 3.34-3.62 s, off 3.20-3.33 s / 3.21-3.32 s;
scheduled-gz on 0.88-1.80 s / 3.35-5.13 s, off 3.39-3.63 s / 3.41-3.52 s.

What the numbers show:

- **Scheduled groups** read the file once instead of once per job (102
  instead of 408 reads plain, 623 instead of 2,492 gzip) and finish about
  3.7 times sooner, because the grouped jobs run together while without
  sharing they run one at a time. dserver CPU does not drop: about 6% higher
  for the plain file (the ranges do not overlap); for gzip the median is 8%
  higher but the ranges overlap, with one outlier `on` run at 5.13 s CPU and
  1.80 s elapsed. The MapReduce work of each job, not the file read,
  dominates it.
- **Follow burst:** the 100 MiB burst evicts all four sessions in every run
  (four `evicted a slow subscriber` lines), which read the burst privately, so
  reads, CPU and time are those of sharing off. In each of the three runs
  whose dserver log was kept (the strace `follow` run and then the separately
  run pair reused the run-1 directories and overwrote their logs), all four
  rejoined the shared reader afterwards (four `rejoined` lines, after a second
  shared read started), so later appends are shared again.
- **Follow paced** (about 10 MiB/s): no evictions; elapsed about 8% lower,
  but dserver CPU about 14% **higher** with sharing on (28.99 s against
  25.50 s, the ranges do not overlap). Sharing a follow read of a busy log
  therefore does not save dserver CPU at N=4 on this host; the cause is not
  yet known (task g9).
