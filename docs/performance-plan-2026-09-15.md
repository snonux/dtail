# DTail performance plan (2026-09-15)

This document records the optimization plan derived from profiling the fork
after the upstream comparison in `BENCHMARK_RESULTS.md`, the baseline numbers
to beat, how to benchmark each change, and a results table to fill in as tasks
complete. Task IDs refer to the `ask` task list of this repository.

**Rule for every task:** the full test suite must succeed after every completed
change. Nothing is done until this passes:

```bash
make clean && make build
make test
DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test
make vet && make lint
```

Every change must also keep output byte-identical (see "How to benchmark").

## Baseline (this machine, 100 MiB inputs)

Measured on 2026-09-15 with the binaries at commit `c472f83`. Host: Rocky 9
bhyve guest, Intel N100, 4 vCPUs, Linux 5.14, clocksource `hpet`.

| Scenario | Lines | Elapsed | User | Sys | Notes |
|---|---:|---:|---:|---:|---|
| dcat server mode | 818,561 | 8.15 s | 2.24 s | 6.93 s | client is syscall bound |
| dcat serverless | 818,561 | 0.30 s | 0.14 s | 0.05 s | 55% in write syscalls |
| dmap aggregate serverless | 820,044 | 1.64 s | 1.68 s | 0.23 s | 398 MB allocated, 133 GCs |
| dgrep ERROR serverless | 818,561 | 0.20 s | 0.11 s | 0.09 s | |

Client CPU profile of the server-mode dcat run: 73% of samples in `time.Now`,
called once per received line from `internal/io/dlog/dlog.go` `Raw`. strace
counted about 1.8M `clock_gettime` calls for one run, two per line. The host
has no vDSO clock, so one `time.Now` costs about 8.3 µs here. The production
r0 to r2 dserver hosts are bhyve guests as well, so this matters in
production and not only in benchmarks. On hosts with a vDSO clock the same
change still removes about 50 ns per line.

Server CPU profile of the same run: 16% in `activityConn.refreshDeadline`
(one `time.Now` plus `SetDeadline` per TCP write), the rest in SSH packet
writes and the read path.

dmap profile: 18% in the regexp backtracker because the dmap line filter
`\|MAPREDUCE:STATS\|` is not recognized as a literal pattern; the remainder
is per-line map and string allocation in the parser and aggregator.

## Plan

Tier 1 targets the server-mode transfer path, tier 2 the dmap CPU path,
tier 3 smaller cleanups. Expected effect column is an estimate from the
profiles, to be replaced by measurements in the results table.

| # | Task | Change | Files | Expected effect |
|---|---|---|---|---|
| 1 | `y4` | Stop calling `time.Now` per payload line in the client logger. Drop the `now` parameter from the `loggers.Logger` sink contract or pass a lazy value; the file sink caches the day string and refreshes it from its existing idle-flush ticker. | `internal/io/dlog/dlog.go`, `internal/io/dlog/loggers/{logger,file,stdout,fout,none}.go` | server-mode dcat 8 s to about 2 s here |
| 2 | `z4` | Rewrite the client receive loop: scan chunks with `bytes.IndexByte`, no per-line `String()` or `+"\n"` allocations, cheap prefix check before `parseAuthKeyMessage`, `[]byte` fast path into the stdout sink using `Write` instead of `fmt.Fprint`. | `internal/clients/handlers/basehandler.go` (`Write`, `handleMessage`, `parseAuthKeyMessage`), `internal/io/dlog/loggers/stdout.go` | remaining client per-line cost roughly halved |
| 3 | `05` | Rate-limit the server idle deadline refresh: atomic activity flag set by Read/Write, per-connection ticker refreshes `SetDeadline` only when activity was seen. | `internal/server/activity_conn.go` | about 16% of server CPU in dcat server mode |
| 4 | `15` | Treat regex patterns whose only metacharacters are backslash-escaped punctuation as literals, so `\|MAPREDUCE:STATS\|` uses `bytes.Contains`. | `internal/regex/regex.go` (`isLiteralPattern`), `internal/clients/query_regex.go` | about 18% of dmap CPU, client and server |
| 5 | `25` | Remove per-line allocations in the aggregator: reuse one fields map per processor, build group keys in a reusable buffer with alloc-free map lookup, avoid the `lineContent.String()` copy, clone only strings retained by `last`/`len` storage. | `internal/mapr/aggregate/aggregate.go`, `internal/mapr/aggregate/groupkey.go`, `internal/mapr/logformat/default.go`, `internal/mapr/aggregateset.go` | large drop in dmap allocations and GC |
| 6 | `35` | Batch lines per `Processor` without a shared mutex per line; take `groupMu` once per batch; check `stopping` once per line. | `internal/mapr/aggregate/{aggregate,batcher}.go` | small dmap CPU win |
| 7 | `45` | `[]byte` fast path from `filteringProcessor.ProcessFilteredRaw` into `DirectLineProcessor` so cat/grep lines skip the pooled buffer round trip. | `internal/io/fs/readfile_processor.go`, `internal/handlers/line_writer.go` | small serverless win |
| 8 | `55` | Hand the 64 KiB batch buffer over instead of copying it twice; make the session generation atomic so `shouldWriteGeneration` takes no mutex per line. | `internal/handlers/line_writer.go`, `internal/handlers/output_manager.go`, `internal/handlers/sessioncommand.go` | small server-mode win |
| 9 | `65` | Evaluate the follow latency floor: 100 ms EOF poll on the server and 100 ms stdout idle flush on the client. Decide with measurements whether to shorten either. | `internal/io/fs/readfile_processor_optimized.go`, `internal/io/dlog/loggers/stdout.go` | follow benchmark only |
| 10 | `75` | Closure: rerun the full benchmark, fill in the results table below, update `BENCHMARK_RESULTS.md`. | this document | |

Not planned: SSH cipher tuning. The server uses the x/crypto defaults,
AES-GCM was under 2% of server CPU, and packet size and window are fixed by
the library.

## How to benchmark

Quick check for a single change, on this machine:

1. Build: `make clean && make build`.
2. Generate data once (the same generators as the benchmark script):
   the `_generate_normal_data` and `_generate_stats_data` awk snippets in
   `benchmarks/upstream_vs_local_bench.sh`, 100 MiB each.
3. Start a server with pprof:
   `cd <dir with cache/> && ./dserver --cfg none --logger stdout --logLevel error --bindAddress 127.0.0.1 --port 2299 --pprof 127.0.0.1:6099`
   with `cache/<user>.authorized_keys` and `cache/ssh_host_key` prepared as
   `_prepare_ssh_material` does.
4. Time the client and record user/sys time, for example with
   `benchmarks/measure_command.py --stdout out.txt --stderr err.txt -- <client command>`.
   Client command shape:
   `DTAIL_SSH_PRIVATE_KEYFILE_PATH=<key> ./dcat --cfg none --plain --noColor --logger stdout --logLevel error --trustAllHosts --servers 127.0.0.1:2299 --files <file>`.
   Add `--cpuprofile --profiledir <dir>` for a client profile and
   `curl -o server.prof "http://127.0.0.1:6099/debug/pprof/profile?seconds=N"`
   for a server profile; inspect with `go tool pprof -top`.
5. Verify correctness: `cmp out.txt <input file>` must report no difference
   for dcat; for dgrep compare against `grep`; for dmap compare the
   canonicalized table against the pre-change output.

Full comparison against upstream and the fork's own previous commit:

```bash
benchmarks/upstream_vs_local_bench.sh smoke                # correctness only
benchmarks/upstream_vs_local_bench.sh run --iterations 5   # timings
```

The script requires a clean tracked tree, an upstream checkout at
`../dtail-mimecast`, and sudo for cache dropping. To compare against the
fork's own baseline instead of upstream, check out commit `c472f83` into a
second directory and pass it as `--upstream-root`.

What to watch per tier: tier 1 should push client sys time toward zero;
tier 2 should reduce the `total_alloc` value printed in the client's
shutdown metrics line well below 398 MB and the `num_gc` count accordingly.

## Results

Fill in after each task completes. Use the same data files and machine as the
baseline, and note the commit.

| Task | Commit | Scenario | Before | After | Verified identical output | Tests pass |
|---|---|---|---:|---:|---|---|
| `y4` | parent `c472f83` | dcat server mode, `--logger stdout` (3 runs, elapsed / user / sys) | 8.54-10.59 s / 2.32-2.40 s / 7.09-7.74 s | 1.24-1.42 s / 1.10-1.14 s / 0.62-0.71 s | yes, `cmp` against input every run | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `y4` | parent `c472f83` | dcat server mode, default `fout` logger | 8.90 s / 2.43 s / 7.36 s | 1.42 s / 1.21 s / 0.70 s | yes, `cmp`; daily log file gets no payload | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `y4` | parent `c472f83` | dcat server mode, `fout --log-payload` | not measured | 3.91 s / 3.47 s / 3.61 s | yes, stdout and file tee both identical to input | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |

`y4` notes: both binaries ran against the same `c472f83` dserver on port 2299
in one session; a final baseline rerun (9.01 s) confirmed no machine drift.
The remaining `--log-payload` cost is the file sink's per-line channel send and
allocation, which this task did not change.
