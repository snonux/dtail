# Performance follow-up, 2026-09-23

Status: validation and the evidence archive are complete; fresh independent
follow-up review found no remaining actionable issues. This is the final
performance acceptance report for task `3a`, covering application `368b29e`.

## Revisions and controls

- Pre-plan control: `d2774f799521d39c282a6113964358d59ad8253d`.
- Final application: `368b29ef39e97635bbc7a49422a5124c2ab065be`.
- Latest recorded shared-read baseline:
  `1a1fc0fe39a9f76fec6ce68f938e854cac0cbb8a`.
- Superseded candidate: `6b8b31342923fe8e57434d70ee134de86213516c`.
  Its initial results exposed the serverless tee regression fixed by `5a`.

This is a local before/after comparison, not a fresh upstream comparison.
The `upstream` label in the full-suite CSV means the pre-plan control above.
The historical upstream `91d3500` versus local `df52a1e` results in
`BENCHMARK_RESULTS.md` are separate evidence and are not used to attribute
these changes.

Host: Rocky Linux 9 bhyve guest, Intel N100, four vCPUs, Linux
5.14.0-687.42.1.el9_8.x86_64, HPET clocksource, Go 1.26.6 linux/amd64.
Both application revisions were compiled from clean tracked trees with
`GOFLAGS= GOMAXPROCS=1 go build -p=1 -trimpath -buildvcs=false`.
Runtime uses four CPUs; targeted and sparse runs explicitly set
`GOMAXPROCS=4`. Measurements, tests and profiles run serially. The other Codex
was checked idle; no process was paused and no resume is owed.

The full suite drops filesystem caches before each observation through the
root-owned `/usr/local/sbin/drop-caches` helper, failing on an unsuccessful
drop and recording receipts. Targeted real-output checks use warm caches,
with an excluded warmup pair and six alternating-order pairs per case.
These are buffered file writes, not `fsync`/durability measurements.

## Implemented changes and their scope

| Task / commit | Change | Supported scope |
|---|---|---|
| `4a` / `316dde3` | Balance skipped-path worker accounting | Correctness prerequisite; no speedup claim |
| `y9` / `a9ac01b` | Start immutable shared chunks at 4 KiB, grow once to the unchanged 64 KiB publication threshold, reserve bounded line metadata | Sparse allocation reduction; no claim that it fixes paced fan-out CPU |
| `z9` / `c6a1aa6` | Filter borrowed bytes before copying for max/after context; reuse a ring for before-context | Context-heavy grep; default-path guards remain important |
| `0a` / `b1c0f83` | Use one backing store for result values, prepare only needed global statistics, and use a non-reflective stable sort | High-cardinality result rendering; all rows and formatting semantics retained |
| `1a` / `7588127` | Suspend inactive file/stdout flush timers and rearm on activity while keeping the original flush phase | Idle CPU on this HPET host, not a universal bulk-throughput gain |
| `2a` / `6b8b313` | Copy file payload into bounded owned batches instead of sending a separate message per line | File-only and opt-in file tee sinks, not default payload output |
| `5a` / `368b29e` | Add optional borrowed-byte file-tee capabilities with legacy string fallbacks | Removes the serverless tee regression exposed during validation |

Representative isolated component results (paired repetitions, not application
speedups): sparse 128-byte chunks allocate 65,648 → 4,320 B/flush; max-context
rejection takes 71.29 → 37.27 ns/line; before-context backing-array churn falls
from 16 → 0 B/line. Rendering 100k count groups with limit 10 takes
285.7 → 158.1 ms. The copying 128-byte file pipeline takes 637.05 → 88.93 ns;
the copying 64 KiB serverless tee adapter takes 21.115 → 2.551 µs and removes
one 64 KiB string allocation. Raw results are under
[the evidence archive](benchmarks/2026-09-23-followup/README.md).

No bounded top-K shortcut was introduced: result ordering, ties, widths,
global statistics and validation of every row remain intact. No SSH cipher
change or shorter follow polling interval was made. Filesystem notifications
remain deferred; this work does not remove the polling/flush latency floor.

## Correctness and verification completed so far

The exact final Go source passed clean build, full unit/race tests, rebuilt
integration/race tests (port base 44000), vet, lint and errcheck under task
`5a`. It also passed an independent fresh-context review. No Go source changed
during `3a`.

The final cold-cache run completed all 110 observations (11 scenarios,
five per revision), all with status `ok`; 114 successful cache-drop receipts
include the smoke checks. All ten saved full 10 MiB follow payloads were
also compared byte-for-byte with their input after stripping only their exact
readiness/end markers; line counts alone were not used as the final oracle.

The final targeted matrix passed all 576 timed observations (48 cases,
six per revision). It checks real stdout and daily-file hashes, cat against
the input (including 1 GiB over SSH and serverless), group counts up to
100,000, mixed full tables and full CSV output. Both default diagnostics-only
logging and opt-in payload teeing are checked. A separate sparse smoke run
checked 20 individual probes plus a 10 MiB burst to all four clients in
shared/private/tee modes on both revisions, with exact stdout/file equality.

Eighteen small harness tests cover real-pipe fragmentation/EOF, missing,
duplicate, reordered and partial records, child cleanup, environment
isolation, empty/unknown selections, and the shared wrapper's order/count and
cross-build parity, fresh-directory and unchanged-binary gates.
The wrapper orchestration tests use a tiny fixture
producer; they are not substitutes for the actual application runs.
Bash syntax and cache-helper failure tests pass. ShellCheck is not installed.
Self-review strengthened the tee-file oracle to reject a missing final newline
instead of normalizing it. All 56 saved sparse tee outputs (48 timed clients
and eight bulk-smoke clients) also pass that stricter exact-byte check.

## Full cold-cache suite

Elapsed medians, seconds, five observations per revision. These small sample
medians are descriptive, not proof of equivalence or universal speedups.

| Scenario | Pre-plan | Final |
|---|---:|---:|
| serverless cat 1 GiB | 1.509026 | 1.493305 |
| serverless cat 100 MiB | 0.201581 | 0.197649 |
| serverless grep high match | 0.193572 | 0.194486 |
| serverless grep low match | 0.212233 | 0.236883 |
| serverless map aggregate | 0.863431 | 0.855738 |
| SSH cat 100 MiB | 0.630174 | 0.642433 |
| SSH grep high match | 0.368018 | 0.372823 |
| SSH grep low match | 0.300595 | 0.311994 |
| SSH map aggregate | 0.976831 | 0.947710 |
| SSH map count | 0.783947 | 0.749064 |
| follow 10 MiB | 0.262866 | 0.247270 |

The low-match serverless grep slowdown did **not** reproduce in ten additional
paired runs with real output checked against `grep`: cold medians
0.223643 → 0.207489 s (ranges 0.198009–0.238243 and 0.197470–0.250152), warm
0.135308 → 0.133667 s (0.129985–0.144814 and 0.126392–0.152446). Retain the
unfavorable first result; the changing direction is evidence of variability,
not a reason to report a grep speedup.

## Targeted real-output results

Selected warm-cache elapsed medians, seconds, six pairs. All underlying cases,
including unfavorable observations, must remain in the evidence archive.

| Workload | Pre-plan | Final |
|---|---:|---:|
| serverless 100 MiB stdout | 0.140742 | 0.141578 |
| serverless 100 MiB default fout | 0.146012 | 0.141035 |
| serverless 100 MiB opt-in tee | 0.329050 | 0.245806 |
| SSH 100 MiB default fout | 0.564500 | 0.565807 |
| SSH 100 MiB opt-in tee | 4.028014 | 0.664757 |
| SSH 100 MiB file-only | 4.059000 | 0.612073 |
| serverless sparse grep, no context | 0.034717 | 0.035868 |
| serverless sparse grep, max | 0.043253 | 0.038847 |
| serverless sparse grep, after | 0.043109 | 0.034909 |
| serverless sparse grep, before | 0.054705 | 0.042856 |
| serverless 100k-group count, top 10 | 1.186856 | 1.123957 |
| serverless 100k-group count, full | 1.290577 | 1.224548 |
| serverless 100k-group mixed, full table | 1.664468 | 1.592478 |
| serverless 100k-group mixed, full CSV | 2.828336 | 2.798703 |

Context fixtures are 32 MiB of 128-byte lines, with a match every 1,000 lines
(sparse) or every two lines (dense). Map fixtures have four records per group.
Small/default-path shifts are not established regressions or equivalence.
The initial 10k-group full count table was 9% slower (0.167037 → 0.182069 s);
ten fresh pairs did not reproduce it: 0.170390 → 0.163039 s, overlapping ranges
0.154606–0.191947 and 0.148420–0.191436.

The large file-sink improvements do not apply to default stdout-only payload
delivery. The formerly regressed serverless tee path is now faster than the
pre-plan control in both its dedicated ten-pair check and this fresh matrix.

Ranges and resource costs for the principal application gains (six observations
per build). CPU is client user + system time; in serverless mode it covers the
whole application. RSS is the process's peak resident memory, not live Go heap.

| Workload | Elapsed range before → after, s | Median client CPU before → after, s | Median peak RSS before → after, MiB |
|---|---|---:|---:|
| serverless 100 MiB tee | 0.286–0.352 → 0.234–0.292 | 0.664 → 0.390 | 21.60 → 16.79 |
| SSH 100 MiB tee | 3.533–4.315 → 0.633–0.672 | 7.363 → 1.002 | 24.71 → 26.64 |
| SSH 100 MiB file-only | 3.872–4.402 → 0.595–1.311 | 7.634 → 0.891 | 22.56 → 24.66 |
| serverless sparse before-context | 0.050–0.062 → 0.042–0.044 | 0.065 → 0.047 | 19.27 → 16.38 |
| serverless 100k count top 10 | 1.153–1.288 → 1.059–1.228 | 2.171 → 2.029 | 267.90 → 235.69 |
| serverless 100k mixed full table | 1.597–1.708 → 1.564–1.616 | 2.894 → 2.695 | 329.17 → 314.66 |

There is no universal RSS reduction: the SSH client's peak RSS medians are
slightly higher in file/tee cases despite sharply lower CPU and elapsed time.
The large isolated result-rendering gains translate into much smaller
whole-application improvements because parsing and aggregation still run.

## Allocation diagnostics

Separate, single profiled replays of the same application commands; no timing
claims use these runs. Runtime TotalAlloc counters are used for total bytes,
not sampled allocation-profile totals. The logger prints `MB`, but divides by
1024², so these values are MiB. GC counts are per profiled process.

| Workload | Total allocated MiB, before → after | GCs, before → after |
|---|---:|---:|
| sparse grep no context | 2.87 → 2.91 | 0 → 0 |
| sparse grep max | 2.88 → 2.88 | 0 → 0 |
| sparse grep before | 6.86 → 2.90 | 2 → 0 |
| 100 MiB serverless tee | 115.44 → 3.37 | 37 → 1 |
| 100k-group count top 10 | 372.21 → 336.70 | 10 → 9 |
| 100k-group mixed full | 502.43 → 453.26 | 11 → 10 |

Shutdown heap allocation is sensitive to GC timing and is not peak retained
memory. Use the recorded process peak RSS and post-GC heap profiles for those
distinct questions; profile sampling and profiler overhead limit precision.
The tiny grep CPU captures are too short for confident CPU attribution.

## Sparse follow and idle cost

Six interleaved pairs per mode, four clients, 20 newline-terminated 128-byte
probes per observation; every client received every probe exactly once and in
order. Payload-tee files also matched; default fout did not log payload.
Probe spacing uses a fixed random sequence between 250 and 450 ms after each
delivery. Start load1 ranged from 0.12 to 0.60. These measurements ran without
bulk traffic, profiles or other benchmark/test processes.

Medians of the six per-observation latency summaries, milliseconds:

| Mode | Median latency, before → after | p95, before → after | Maximum, before → after |
|---|---:|---:|---:|
| shared, default fout | 102.00 → 107.23 | 167.57 → 169.82 | 185.48 → 190.57 |
| private, default fout | 102.89 → 106.11 | 168.66 → 169.10 | 188.13 → 185.98 |
| shared, payload tee | 103.98 → 101.85 | 168.70 → 170.53 | 192.34 → 189.86 |

The maximum column is a median of maxima, **not** a global bound. Observed
individual maxima reached 200.61 ms before and 200.09 ms after. There is no
demonstrated latency improvement: polling and flushing still impose roughly
a 100 ms median / 170 ms p95 floor. Small shifts and overlapping ranges are
not proof of equivalence.

Two-second idle windows sample process CPU ticks for the server plus its
0/1/4 clients. Values below are median percent of **one core**, not percent
of all four CPUs. At CLK_TCK=100, one tick over two seconds is about 0.5
percentage points; a zero means no sampled ticks, not literally zero work.

| Mode | No clients, before → after | One client, before → after | Four clients, before → after |
|---|---:|---:|---:|
| shared, default fout | 1.50 → 0.00 | 4.00 → 1.75 | 8.74 → 2.50 |
| private, default fout | 1.50 → 0.25 | 3.75 → 1.50 | 11.98 → 4.74 |
| shared, payload tee | 1.50 → 0.00 | 4.00 → 1.50 | 9.74 → 2.25 |

These are whole-process idle costs on HPET, not portable logger-only CPU
claims. File polling and SSH activity timers remain. Server peak RSS medians
were about 18–21 MiB and summed client high-water marks about 66–68 MiB;
the latter sum is not a simultaneous aggregate peak. The raw CSV retains
per-observation peak/current server RSS and client high-water sums.

## Shared reads against both controls

Fresh six-round comparison of `d2774f7` (pre-plan), `1a1fc0f` (latest recorded
baseline), and `368b29e` (final), N=4, 100 MiB. All 144 observations passed
within-build output checks; all scheduled job CSVs also matched across builds,
rounds and modes. Binary hashes matched before and after the full run. All six
build permutations were used, with each revision first/middle/last twice;
sharing-mode order alternated each round. No builds, tests or profiles overlapped.

Each run waited for load1 below 1.0 and no other benchmark/test server before
startup. The CSV's follow load is sampled **after** SSH client bootstrap, so
some recorded loads exceed 1.0; it is not the preflight load. These are warm-cache
shared-read workloads, not the cache-dropped full suite above.

Cells show median elapsed / dserver CPU seconds, six observations each.

| Workload / sharing | Pre-plan | Latest baseline | Final |
|---|---:|---:|---:|
| burst off | 4.775 / 17.025 | 4.725 / 16.945 | 4.695 / 16.895 |
| burst on | 4.685 / 16.770 | 4.690 / 16.825 | 4.680 / 16.935 |
| paced off | 12.435 / 26.085 | 12.490 / 26.570 | 12.510 / 26.375 |
| paced on | 12.290 / 29.040 | 12.190 / 29.480 | 12.030 / 29.165 |
| scheduled plain off | 3.125 / 3.120 | 3.215 / 3.150 | 3.110 / 3.090 |
| scheduled plain on | 0.875 / 3.340 | 0.890 / 3.370 | 0.905 / 3.270 |
| scheduled gzip off | 3.325 / 3.345 | 3.405 / 3.360 | 3.265 / 3.235 |
| scheduled gzip on | 0.890 / 3.385 | 0.875 / 3.385 | 0.890 / 3.270 |

The paced shared CPU penalty **remains**: final medians are 29.165 vs 26.375 s
(10.6% more CPU); pre-plan and latest-baseline gaps are 11.3% and 11.0% in
this same run. Sparse allocation savings did not remove the fan-out cost.
Paced shared CPU ranges are 27.38–29.47, 28.67–29.70 and 28.71–29.32 s
respectively; final elapsed spans 11.69–12.68 s shared and 12.27–12.87 s private.
Small cross-revision median shifts do not establish a further throughput win.

Every untraced shared burst evicted all four sessions in every revision.
It therefore mostly becomes private reading, not a demonstration of effective
sharing. Final burst ranges overlap: 4.66–4.77 s shared, 4.67–4.84 s private.
The latest-baseline shared burst outlier (6.08 s, 21.34 CPU s) is retained.

Scheduled sharing remains useful: final plain/gzip jobs finish about 3.4/3.7
times sooner than sharing off. **This was already present in both controls**,
and compares a parallel scheduled group to four serial jobs, not just one I/O
implementation to another. Plain final shared CPU is about 5.8% higher than
private; gzip is about 1.1% higher. Final shared elapsed ranges are 0.87–0.91 s
plain and 0.87–1.07 s gzip. The gzip 1.07 s / 4.15 CPU s observation and the
latest-baseline plain 1.49 s observation remain in the raw data.

Separate one-round syscall diagnostics also passed all 24 output checks,
cross-build job comparisons and unchanged-binary checks. They used `strace`
only on dserver, tracing `read`, `pread64`, `readv` and `preadv` with descriptor
paths. Counts below are calls on the input file, **not bytes**, and include
EOF/retry reads. Total reads (including sockets) remain in the raw CSV.
Traced elapsed/CPU fields are deliberately `n/a`.

| Workload / sharing | Pre-plan file reads | Latest baseline | Final |
|---|---:|---:|---:|
| burst off | 6,436 | 6,434 | 6,432 |
| burst on | 5,977 | 6,160 | 5,757 |
| paced off | 6,593 | 6,605 | 6,600 |
| paced on | 1,687 | 1,698 | 1,687 |
| scheduled plain off | 408 | 408 | 408 |
| scheduled plain on | 102 | 102 | 102 |
| scheduled gzip off | 2,492 | 2,492 | 2,492 |
| scheduled gzip on | 623 | 623 | 623 |

Paced sharing cuts read calls roughly fourfold, but fan-out costs more CPU
in the untraced run. Shared bursts evict all four clients even under tracing;
their varying counts reflect how far the shared reader gets before eviction,
not a repeatable recent-change win. Scheduled groups read/decompress one
input instead of four in every revision. Single traced observations are
mechanism evidence, not statistically established performance differences.

## Acceptance review

The first independent review checked the harness and recalculated reported
counts, hashes, latencies and shared medians. It also checked 56 actual sparse
tee files, ten full follow outputs and 336 scheduled-job CSVs. Its one finding
was in the cache-control test's fallback-path simulation, not production
cache handling. That test now checks both helper-selection branches on every
host, including failures. All 18 Python tests and Bash checks passed again.
A fresh follow-up review confirmed the resolution and found no remaining
actionable issues. Review scope and unavailable tools are recorded in
[verification notes](benchmarks/2026-09-23-followup/verification/checks.md).

## Conclusions supported by the completed timing runs

- Keep the sparse chunk and context-filter changes for their measured
  allocation/control-cost reductions. Before-context also shows a real
  application benefit; sparse sharing is not a general fan-out throughput fix.
- Keep result preallocation and typed stable sorting. Large count/top-ten
  rendering is substantially cheaper; mixed full results have smaller or
  inconclusive gains. Full materialization still preserves widths, statistics,
  error handling and stable ordering; no top-K shortcut was justified here.
- Keep idle timer suspension with its original flush phase. Idle CPU falls on
  this HPET host, but sparse latency does not improve. Do not extrapolate these
  CPU percentages to hosts with cheap user-space clock reads.
- Keep bounded file batches and the borrowed-byte tee fix. The clearest
  end-to-end gains are opt-in payload tee and file-only output. An earlier
  serverless tee regression was reproduced, fixed as separate task `5a`, and
  rechecked against both the regressed build and original pre-plan control.
- Do not claim a broad default-output speedup or formal performance
  equivalence. Small median changes, the non-reproduced grep/10k-map outliers,
  and all unfavorable observations are retained rather than filtered away.
- No further shared-follow CPU or latency optimization is accepted by this
  evidence. The paced CPU penalty and polling/flush floor remain. Portable
  filesystem notifications and their rotation/overflow/NFS semantics remain
  deferred; changing SSH ciphers or shortening polling was outside this plan.

These are workload-specific acceptance conclusions, not a claim that every
code path is faster or that the remaining shared CPU and latency costs are fixed.
