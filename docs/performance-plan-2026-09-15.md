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
| dcat server mode | 818,561 | 8.15 s | 2.24 s | 6.93 s | client is syscall bound; single run, re-measured as 8.54-10.59 s before `y4` (see Results) |
| dcat serverless | 818,561 | 0.30 s | 0.14 s | 0.05 s | 55% in write syscalls |
| dmap aggregate serverless | 820,044 | 1.64 s | 1.68 s | 0.23 s | 398 MB allocated, 133 GCs |
| dgrep ERROR serverless | 818,561 | 0.20 s | 0.11 s | 0.09 s | |

Client CPU profile of the server-mode dcat run: 73% of samples in `time.Now`,
called once per received line from `internal/io/dlog/dlog.go` `Raw`. The
vDSO is mapped on this host and Go takes its vDSO path, but the clocksource
is `hpet`, which the vDSO cannot read in user space, so the kernel's vDSO
code falls back to a real syscall. Both `clock_gettime` calls Go makes per
`time.Now` (`CLOCK_REALTIME` then `CLOCK_MONOTONIC`, see the Go runtime's
`time_linux_amd64.s`) therefore become real syscalls. strace counted about
1.8M `clock_gettime` calls for one run: 2 x 818,561 = 1.64M from `Raw`; the
remaining ~0.16M were not attributed (runtime `nanotime` reads and timers
are the likely source). One `time.Now` (both calls, user and kernel time
together) costs about 8.3 µs here, measured separately on 2026-09-15. Over
818,561 lines that is about 6.8 s, which roughly accounts for the drop in
client sys time from before to after `y4` (7.09-7.34 s to 0.62-0.71 s, see
Results). It is not the whole sys time: part of the 8.3 µs is user time,
and after the change sys is still 0.62-0.71 s from the unattributed clock
reads and network syscalls.
The production r0 to r2 dserver hosts are bhyve guests as well, so this
matters in production and not only in benchmarks. On hosts whose clocksource
the vDSO can read (for example `tsc`) the same change still removes about
50 ns per line.

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
| `y4` | parent `c472f83` | dcat server mode, `--logger stdout` (3 runs, elapsed / user / sys) | 8.54-10.59 s / 2.32-2.40 s / 7.09-7.34 s | 1.24-1.42 s / 1.10-1.14 s / 0.62-0.71 s | yes, `cmp` against input every run | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `y4` | parent `c472f83` | dcat server mode, default `fout` logger | 8.90 s / 2.43 s / 7.36 s | 1.42 s / 1.21 s / 0.70 s | yes, `cmp`; daily log file gets no payload | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `y4` | parent `c472f83` | dcat server mode, `fout --log-payload` | not measured | 3.91 s / 3.47 s / 3.61 s | yes, stdout and file tee both identical to input | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `z4` | parent `85475b5` | dcat server mode, `--logger stdout` (5 interleaved runs, elapsed / user / sys) | 1.26-1.31 s / 1.09-1.19 s / 0.58-0.64 s | 0.82-0.97 s / 0.33-0.37 s / 0.43-0.51 s | yes, `cmp` against input every run | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `z4` | parent `85475b5` | dcat server mode, default `fout` logger (5 interleaved runs) | 1.30-1.36 s / 1.09-1.18 s / 0.58-0.67 s | 0.82-0.99 s / 0.36-0.41 s / 0.44-0.49 s | yes, `cmp` against input every run | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `z4` | parent `85475b5` | client profile, dcat server mode, `--logger stdout` | 1.57 s CPU samples, 273 MB total_alloc, 76 GCs | 0.71 s CPU samples, 128 MB total_alloc, 60 GCs | yes | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `z4` | parent `85475b5` | `BenchmarkBaseHandlerWrite`, 32 KiB chunk of 136-byte lines | 201 µs/op, 163 MB/s, 510 allocs/op | 10 µs/op, 3.28 GB/s, 0 allocs/op | n/a (unit tests compare against the legacy loop) | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `05` | parent `59356ae` | dserver CPU (utime+stime from `/proc`) per dcat server-mode run, `--logger stdout` (5 interleaved runs) | 1.25-1.34 s | 1.02-1.07 s (one outlier 1.33 s) | yes, `cmp` against input every run | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `05` | parent `59356ae` | client elapsed / user / sys, same runs | 0.85-0.97 s / 0.32-0.39 s / 0.43-0.49 s | 0.72-0.87 s / 0.31-0.40 s / 0.39-0.50 s | yes | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `05` | parent `59356ae` | server CPU profile, dcat server mode | `activityConn.refreshDeadline` 19% of samples (`time.Now` 8%, `SetDeadline` 7%) | no deadline refresh in the profile | yes | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `15` | parent `b0340f5` | dmap aggregate serverless, 100 MiB stats log (3 interleaved rounds, elapsed / user / sys) | 1.82-1.87 s / 2.07-2.13 s / 0.30-0.36 s | 1.52-1.72 s / 1.82-2.02 s / 0.36-0.39 s | yes, raw `cmp` and canonicalized table every round | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `15` | parent `b0340f5` | dmap count serverless, same log (2 interleaved rounds) | 1.50-1.62 s / 1.78-1.93 s / 0.33-0.40 s | 1.30-1.46 s / 1.62-1.80 s / 0.42-0.43 s | yes, raw `cmp` and canonicalized table every round | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `15` | parent `b0340f5` | client CPU profile, dmap aggregate serverless | `regexp.(*Regexp).backtrack` 14.3% cumulative of 1.75 s samples | no `regexp` samples left in the profile (1.68 s samples) | yes | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |
| `15` | parent `b0340f5` | `BenchmarkMaprFilterPattern`, `\|MAPREDUCE:STATS\|` against 3 lines per op | 702-747 ns/op (compiled regexp) | 403-411 ns/op (literal search) | n/a (unit tests compare the literal against regexp) | yes: make clean && make build && make test && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test && make vet && make lint |

`y4` notes: both binaries ran against the same `c472f83` dserver on port 2299
in one session; a final baseline rerun (9.01 s / 2.42 s / 7.74 s, not part
of the 3-run range) confirmed no machine drift.
The remaining `--log-payload` cost is the file sink's per-line channel send and
allocation, which this task did not change.

`z4` notes: the before binaries were built from `85475b5` and both clients
ran against that commit's dserver on port 2299, alternating before and after
in each of the 5 rounds. Three earlier after-runs made right after `make
test` measured 1.42-1.46 s / 0.66-0.71 s / 0.93-0.97 s; the interleaved
rounds did not reproduce that, so it is treated as disturbance from the
finishing test run. The client-side `baseHandler.Write` share of client CPU
fell from 46% to about 15% (cumulative); what remains is
mostly network read syscalls and runtime clock and scheduler work. dgrep
(`--regex ERROR`), non-plain dcat and dmap output were also compared against
the before client and are identical.

`05` notes: before and after dservers were built with `go build` from
`59356ae` and from the change, ran side by side on ports 2299 and 2300, and
each round ran the before client against the before server and then the
after client against the after server. Server CPU was read from
`/proc/<pid>/stat` around each client run. The round-2 after run (1.33 s
server CPU, 0.87 s elapsed) was not reproduced in the other rounds or in a
sixth profiled round (1.02 s); it is treated as noise. Read and Write now only
set an atomic flag; a per-connection ticker (timeout/4, clamped to 1-30 s)
extends the deadline when activity was seen, so no idle gap up to the
timeout closes an active session. The original commit (`95234cc`) used
now + timeout + interval and described the idle close as "between the
timeout and the timeout plus two intervals"; the real window was timeout +
interval to timeout + 2 intervals, and that extension left no scheduling
margin at any timeout: a tick handled one interval late landed exactly on the
deadline whenever the timeout was a multiple of the interval (900 s, 61 s,
5 s, 2 s) and past it otherwise (901 s missed by 29 s), so timeout mod
interval only decided how badly a late tick missed, never whether it missed.
The review follow-up extends to now + timeout + 2 intervals, which tolerates a
tick handled less than one interval late for any timeout (a tick exactly one
interval late would refresh on the deadline, racing the poller); an idle session
now closes between timeout + 2 intervals and timeout + 3 intervals after its
last activity (about 960-990 s for the default 900 s timeout). The 1 s floor on the
interval keeps the refresher from waking sub-second, at the price of a
disproportionate window for timeouts under 4 s (they close 2-3 s late); that
trade-off is documented at config.DefaultIdleSessionTimeoutS and pinned by a
test. The remaining `activityConn.Write` cumulative share (28%)
is the underlying TCP write syscall.

`15` notes: the before binary was built from `b0340f5`; before and after ran
alternately in each round against the same 100 MiB `_generate_stats_data` file,
serverless (`--cfg none --plain --noColor --logger stdout --logLevel error`).
A pattern is now treated as a literal when its only metacharacters are
backslash-escaped ASCII punctuation, so the dmap line filter
`\|MAPREDUCE:STATS\|` unescapes to `|MAPREDUCE:STATS|` and is matched with
`bytes.Contains`. Escapes with a meaning of their own (`\d \w \s \D \W \S
\b \B \A \z \Q \E \n \t \a \f \r \v \x41 \x{263a} \p{L}`, octal
escapes, and every other alphanumeric escape), unescaped metacharacters, an
escaped space, a trailing backslash, and patterns holding U+FFFD or invalid
UTF-8 all keep the compiled regexp, which stays compiled in either case as the
fallback.

For a pattern holding a validly encoded U+FFFD (the bytes `ef bf bd`) this is a
change of behaviour, not only of speed: the old `isLiteralPattern` rejected just
`.+*?^$[]{}()|\`, so such a pattern took the `bytes.Contains` path and
`dgrep --grep $'abc\xef\xbf\xbd'` returned only the line holding those exact
bytes. It now takes the compiled regexp, which maps every decoding error in the
input to U+FFFD as well, so the same pattern also returns lines holding
`abc\xff` and `abc\xfe`: a superset of what it matched before. Checked end to
end against a four line file with `dgrep` built from `b0340f5` and from this
tree. The regexp semantics are the reference and the intended behaviour, but a
mixed-version fleet can return different lines for a U+FFFD-bearing pattern
until every host is upgraded.

A pattern holding invalid UTF-8 is not part of that change. `regexp.Compile`
rejects such a pattern outright (`error parsing regexp: invalid UTF-8`), and the
old `newRegex` compiled the pattern on its literal path as well and returned the
same error, so `regex.New("abc\xff", Default)` failed before this change just
as it does after it. `dgrep --grep $'abc\xff'` exits 1 with that compile error
in both versions, because `internal/clients/baseclient.go` makes it fatal;
checked with both binaries. There is no behaviour change and no mixed-version
hazard for invalid-UTF-8 patterns. An earlier revision of this note, and the
message of commit `a8e5e0c`, used `$'abc\xff'` as the example of the widened
match; that was wrong, only U+FFFD-bearing patterns widen.

Correctness is pinned by a table test, by a check of every ASCII escape against
`regexp` itself, by a seeded randomized differential test, and by
`FuzzLiteralPattern`, whose body skips patterns over 1 KiB and inputs over
4 KiB. The bound exists because the reference side of the property
(`regexp.MatchString` and `regexp.Match` on an accepted literal) costs
O(len(pattern) * len(input)) in the worst case, and reaching that worst case
needs an input which keeps re-entering a partial match. The cost is therefore a
property of the pair, not of the size alone. A 64 KiB pattern (`\|` repeated
32768 times) against 64 KiB of `|`, where every position starts a partial match,
spends about 27.5 s matching and starves the fuzzer for the rest of the run once
it is in the corpus. The same pattern against 64 KiB of `a` costs about 12 ms
for one execution of the fuzz body, which recompiles the pattern every time, and
that figure is almost entirely `regexp.Compile` (8-15 ms on its own here) rather
than matching: the two matches on the already compiled regexp take a few tens of
microseconds. At the bound (1 KiB pattern, 4 KiB input) even the adversarial
pair costs single-digit milliseconds.

Fuzz runs use `go test -run XXXnone -fuzz FuzzLiteralPattern -fuzztime 60s
./internal/regex/` and have never failed. Absolute execution totals are not
comparable across runs, because the shared corpus grows between them (211
entries when these notes were first written, 239 now); only runs against the
same corpus state are. Against identical copies of the 239-entry corpus, bounded
runs and an unbounded control all landed between roughly 0.7M and 1.2M
executions per 60 s window, which is the run-to-run spread of the bounded body
by itself, so the bound buys no measurable throughput here; it only guards
against the pathological pair above. A run can also sit at 0/sec for a long
stretch. That is typically the engine minimizing a newly interesting input, a
phase which does not advance the execution counter and which reproduces readily
on a cold corpus, where such inputs are still being found; but a warm run can
stall for about 10 s and still add no input at all, so a stall on its own does
not mean one was found. Per-run execution totals and a ~50 s figure for the
64 KiB pair recorded in earlier revisions of this note did not reproduce and are
withdrawn.

The `literal` hint in the serialized form is now only emitted for patterns which
are their own literal: an older peer trusts that hint verbatim and would search
for the backslashes, while peers of this version derive the literal from the
pattern themselves, so server-mode dmap gets the same optimization without the
hint.
