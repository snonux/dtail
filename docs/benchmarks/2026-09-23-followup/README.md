# Follow-up evidence (task 3a)

Measurements and archive complete; fresh independent follow-up review passed.
See [the report](../../performance-followup-2026-09-23.md) for scope, limits
and interpretation. No private keys, binaries, generated log payloads or binary
profiles are committed here.

## Contents

- `full-final`: five-pair cold-cache raw results, generated summary, exact
  revision/compiler/binary/input metadata, and privileged cache-drop receipts.
  Here `upstream` means local pre-plan `d2774f7`, **not** Mimecast upstream;
  `local` means final `368b29e`. `summary.csv`'s positive change percentage is
  `(before / after - 1) * 100`, not percent elapsed-time reduction.
- `targeted-final`: six-pair warm real-output matrix and the exact commands
  (including unreported round-zero warmups). Each row includes hashes checked
  during execution. Output files were deleted only after verification; inputs
  remain reproducible from the generators. RSS is the client's peak RSS.
- `sparse-final`: six pairs per mode, per-process CPU/RSS summaries and every
  individual probe latency. Idle CPU fields are **seconds**, not percentages.
  `clients_peak_rss_sum_kib` adds process high-water marks, not simultaneous
  memory use. `server_rss_kib` is a current snapshot, not a peak.
- `shared-final`: six rounds of three builds, all four workloads and both
  sharing modes (144 observations); order, build metadata/checksums, complete
  run log, and four canonical job CSVs. Its `results.csv` files have no header:
  `scenario,input,mode,run,sessions,elapsed_s,cpu_s,file_reads,all_reads,evictions,shared_log,load1,output_ok`.
  Cross-build comparisons cover every job/round/mode and both input formats.
- `shared-trace-final`: separate one-round syscall diagnostics (24 observations),
  with the same CSV schema and `n/a` timing/CPU fields. Counts are file-read
  calls, including EOF/retries, not bytes. Metadata, hashes and run log retained;
  large raw strace files are reproducible and not committed.
- `grep-low-final`, `map10k-final-confirm`: ten-pair repeats of apparent
  regressions that did not reproduce. The original unfavorable results remain
  in the main data sets. Grep output matched the independent grep oracle.
- `superseded-full`, `superseded-targeted`: earlier `6b8b313` candidate results,
  not measurements of final `368b29e`. These exposed the serverless tee issue.
- `5a-vs-preplan`, `5a-vs-regression`: ten-pair real-file checks of the `5a`
  production source against `d2774f7` and `6b8b313`, respectively; the binary
  directory was named `fix5a` before its commit. The fresh `targeted-final`
  measurements verify the subsequently committed final build independently.
- `components`: final accepted paired microbenchmarks and benchstat summaries
  for y9, z9, 0a, 1a, 2a and 5a. Do not treat component ns/op as an application
  throughput gain. `1a-phase-idle-*` are the separate logger-only idle probes,
  unlike the whole-process sparse-follow measurements.
- `profiles-final`: exact profiled commands, runtime allocation/GC counters,
  process measurements and pprof text summaries. These are single diagnostic
  replays, **excluded from timing comparisons**. CPU sample counts can be very
  small; sampled profiles locate costs, runtime TotalAlloc measures total
  allocated bytes. The original binary profiles are reproducible, not needed
  for reading these summaries. Heap profiles are post-GC, not peak memory.
- `verification/final-go-gates.log`: clean build, unit and rebuilt integration
  race tests, vet and lint on the final Go source under `5a`; the full command
  also ran `errcheck ./...` silently and returned zero. No Go code changed in
  `3a`. The harness has its own tests and actual-output acceptance runs.
- `verification/harness-tests.log`: the final 18 safety/oracle tests. The
  stricter tee oracle was also applied successfully to all 56 saved sparse
  client log outputs (timed and bulk-smoke); see `verification/checks.md`.

Paths inside raw commands/metadata identify the original scratch directory.
They are provenance, not required permanent paths. ShellCheck was unavailable;
Bash syntax, explicit failure tests, Python tests and actual application runs
were used instead. Review checks and the resolved test-only finding are recorded
in [verification/checks.md](verification/checks.md). The archive's CSV files
are intentionally included despite the repository's general CSV ignore rule.
Raw CSVs retain Python's CRLF record endings; the two full-suite metadata files
retain the trailing blank `go env` field. Git's whitespace checker flags those
generated records. They are preserved as evidence; authored source and Markdown
pass the staged whitespace check separately.

## Reproduce application comparisons

Run serially on an otherwise quiet Linux host with Go 1.26.6, the project's
normal build dependencies, ssh-keygen, GNU time/timeout utilities, Python 3,
iproute/ss, strace and the privileged root-owned cache helper. Do not silently
skip cache control. Worktrees should be clean; preserve unrelated user work.

```bash
set -euo pipefail
repo=$(pwd -P)
workdir=$(mktemp -d /tmp/dtail-followup.XXXXXXXX)
git worktree add --detach "$workdir/preplan" d2774f799521d39c282a6113964358d59ad8253d
git worktree add --detach "$workdir/latest-baseline" 1a1fc0fe39a9f76fec6ce68f938e854cac0cbb8a
git worktree add --detach "$workdir/final" 368b29ef39e97635bbc7a49422a5124c2ab065be
for revision in preplan latest-baseline final; do
    (
        cd "$workdir/$revision" || exit
        for binary in dserver dcat dgrep dmap dtail; do
            GOFLAGS= GOMAXPROCS=1 go build -p=1 -trimpath -buildvcs=false \
                -o "$binary" "./cmd/$binary" || exit
        done
    ) || exit
done

bash benchmarks/upstream_vs_local_bench.sh run \
    --upstream-root "$workdir/preplan" --local-root "$workdir/final" \
    --workdir "$workdir/full" --iterations 5

GOMAXPROCS=4 PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/perf_followup_cases.py \
    --before "$workdir/preplan" --after "$workdir/final" \
    --workdir "$workdir/targeted" --data-dir "$workdir/full/data" \
    --key "$workdir/full/server/auth_key" --iterations 6

# Run only after timing has stopped; these are diagnostics.
GOMAXPROCS=4 PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/profile_followup_cases.py \
    --commands "$workdir/targeted/commands.jsonl" --workdir "$workdir/profiles"

GOMAXPROCS=4 PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/sparse_follow_compare.py \
    --before "$workdir/preplan" --after "$workdir/final" \
    --workdir "$workdir/sparse" --key "$workdir/full/server/auth_key" --iterations 6

# The wrapper alternates build and sharing-mode order and checks cross-build CSVs.
GOMAXPROCS=4 bash benchmarks/shared_read_compare.sh "$workdir/shared" \
    "$workdir/preplan" "$workdir/latest-baseline" "$workdir/final" 6

# Separate syscall diagnostics, never used for CPU/elapsed comparisons.
TRACE=yes GOMAXPROCS=4 bash benchmarks/shared_read_compare.sh "$workdir/traced" \
    "$workdir/preplan" "$workdir/latest-baseline" "$workdir/final" 1
```

The original full follow outputs can be independently checked after the suite:

```bash
for build in local upstream; do
    for round in 1 2 3 4 5; do
        grep -vE "^(PROBE-$build-$round-yes |BENCHEND-$build-$round-yes$)" \
            "$workdir/full/output/follow-$build-$round-yes.out" \
            | cmp - "$workdir/full/data/follow_10mib.log" || exit
    done
done
```

For a separate sparse smoke/full-byte check, use a fresh workdir,
`--iterations 1 --bulk "$workdir/full/data/follow_10mib.log"`; bulk delivery
happens after probe metrics and must not be treated as a bulk timing result.

Harness safety tests:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s benchmarks -p test_perf_followup.py -v
bash benchmarks/cache_control_test.sh
bash -n benchmarks/shared_read_bench.sh benchmarks/shared_read_compare.sh \
    benchmarks/upstream_vs_local_bench.sh benchmarks/grep_pair_confirm.sh
```

## Reproduce isolated component measurements

The benchmark and idle-probe sources are committed, not temporary-only code.
Use the **after** checkout below for both test binaries, with a Go `-overlay`
JSON replacing the listed production file(s) with their contents at **before**.
This holds benchmark code constant. Do not apply the overlays to today's HEAD:
later tests may depend on methods added by subsequent tasks. Build test binaries
before measuring, then run pairs in alternating order with `GOMAXPROCS=1`,
`-test.run '^$'`, `-test.benchmem` and the indicated bench duration. All except
the y9 packing probe were pinned to CPU 0 using `taskset -c 0`.

| Task / raw prefix | Before → after | Overlay files (relative to repo) | Package / benchmark | Pairs / duration |
|---|---|---|---|---|
| y9 / `y9-` | d2774f7 → a9ac01b | `internal/io/fs/readhub/fanout.go` | `./internal/io/fs/readhub`, `BenchmarkFanout` | 5 / 200ms |
| z9 / `z9-verified-` | a9ac01b → c6a1aa6 | `internal/io/fs/readfile_processor.go` | `./internal/io/fs`, `BenchmarkContextFilter` | 5 / 150ms |
| 0a / `0a-` | c6a1aa6 → b1c0f83 | `internal/mapr/groupset.go` | `./internal/mapr`, `BenchmarkGroupSetResult` | 6 / 150ms |
| 1a / `1a-phase-` | b1c0f83 → 7588127 | `internal/io/dlog/loggers/{stdout,file}.go` | `./internal/io/dlog/loggers`, `BenchmarkLoggerOutput` | 6 / 300ms |
| 2a / `2a-final-` | 7588127 → 6b8b313 | `internal/io/dlog/loggers/file.go`; test adjustments below | `./internal/io/dlog/loggers`, `BenchmarkLoggerOutput` | 6 / 300ms |
| 5a / `5a-` | 6b8b313 → 368b29e | `internal/clients/runtime_boundary.go` | `./internal/clients`, `BenchmarkServerlessPayloadByteTee` | 6 / 300ms |

For `2a` also overlay `file_test.go` and `idle_flush_test.go` from `7588127`,
and replace `file_batch_test.go` with an empty `package loggers` file in the
before binary: those new tests reference queue-only APIs. The identical
`output_benchmark_test.go` remains in both. The newly added queue files may
compile into the before binary but its old file implementation never uses them.
For `z9`, both binaries contain the new stats helper, but the old processor
does not call it. These are isolated component comparisons, not whole old-tree
versus new-tree application builds.

For example, a result-rendering overlay (commands shown for a fresh scratch
directory; substitute the appropriate task row above):

```bash
set -euo pipefail
component=$(mktemp -d /tmp/dtail-component.XXXXXXXX)
git worktree add --detach "$component/tree" b1c0f83
git show c6a1aa6:internal/mapr/groupset.go > "$component/before.go"
printf '{"Replace":{"%s/internal/mapr/groupset.go":"%s/before.go"}}\n' \
    "$component/tree" "$component" > "$component/before.json"
(
    cd "$component/tree" || exit
    GOMAXPROCS=1 go test -p 1 -c -o "$component/before.test" \
        -overlay "$component/before.json" ./internal/mapr
    GOMAXPROCS=1 go test -p 1 -c -o "$component/after.test" ./internal/mapr
)
for round in 1 2 3 4 5 6; do
    order=(before after)
    if ((round % 2 == 0)); then order=(after before); fi
    for build in "${order[@]}"; do
        GOMAXPROCS=1 taskset -c 0 "$component/$build.test" \
            -test.run '^$' -test.bench '^BenchmarkGroupSetResult$' \
            -test.benchmem -test.benchtime=150ms \
            > "$component/$build-$round.txt" || exit
    done
done
```

The separate `1a-phase-idle-*` records use the committed
`internal/io/dlog/loggers/idle_cpu_linux_test.go` `TestIdleLoggerCPU` probe
(0/1/16 idle pairs, three seconds each), the same 1a before overlay, three
alternating pairs, and `GOMAXPROCS=1 DTAIL_PERF_IDLE_PROBE=yes` with
`-test.v -test.run '^TestIdleLoggerCPU$'` (without `-race`). Without that
environment opt-in the test deliberately skips. Do not combine idle-probe CPU with bulk
benchmark timing. Earlier prototypes and unphased timers are not final evidence.

Outlier confirmations are reproducible with fresh output directories:

```bash
bash benchmarks/grep_pair_confirm.sh "$workdir/preplan" "$workdir/final" \
    "$workdir/full/data/normal_100mib.log" "$workdir/grep-confirm" 'user999 ' 10
GOMAXPROCS=4 PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/perf_followup_cases.py \
    --before "$workdir/preplan" --after "$workdir/final" \
    --workdir "$workdir/map-confirm" --data-dir "$workdir/full/data" \
    --key "$workdir/full/server/auth_key" --iterations 10 \
    --case map-10000-count-limit10000 --transport serverless
```
