# Verification scope

Final application source: `368b29ef39e97635bbc7a49422a5124c2ab065be`.
No Go source changed in task `3a`. The archived `final-go-gates.log` is from
the successful task `5a` command (exit 0):

```bash
make clean && make build && make test &&
DTAIL_INTEGRATION_TEST_PORT_BASE=44000 DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test &&
make vet && make lint && errcheck ./...
```

That exact source also passed independent review with race coverage on clients,
clientlog, dlog and loggers; all four changed production functions had 100%
statement coverage. `3a` changes benchmark tooling and documentation only.

Final harness checks passed after the syscall run stopped:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s benchmarks -p test_perf_followup.py -v
bash -n benchmarks/shared_read_compare.sh benchmarks/shared_read_bench.sh \
    benchmarks/upstream_vs_local_bench.sh benchmarks/grep_pair_confirm.sh \
    benchmarks/cache_control_test.sh
bash benchmarks/cache_control_test.sh
git diff --check
```

The cache test deliberately prints `cache drop failed` for its simulated
nonzero sudo result, checks that execution stopped, then reports success.
It does not actually evict host caches. The real full suite separately recorded
114 successful privileged cache-drop receipts. ShellCheck was not installed;
no ShellCheck pass is claimed.

The first independent `3a` review found that the test's simulated sudo accepted
the preferred helper's hyphenated name but rejected the supported fallback
`benchmarks/drop_caches.sh`. The corrected test substitutes only the preferred
path with a temporary executable fixture and checks the exact selected path.
It now exercises both preferred and fallback selection, each with successful
and failed sudo statuses, on every host. Both branches and all 18 Python tests
passed after this test-only correction; production cache control did not change.

Self-review found that the original sparse tee check reconstructed newline
delimiters and could therefore accept a missing final newline. The updated
`verify_tee` compares bytes without repairing the data. Its negative tests
cover missing/duplicate/reordered/partial records and an absent file set.
All 56 original client log outputs were rechecked successfully: 12 timed
tee observations × 4 clients plus two bulk-smoke observations × 4 clients.
Reproduce that check with the original scratch artifacts (or newly generated
ones following the README):

```python
from pathlib import Path
from sparse_follow_compare import verify_tee  # put benchmarks/ on PYTHONPATH

root = Path('/tmp/dtail-3a-4GFdYdUn')
count = 0
for suite in ('sparse-final', 'sparse-smoke'):
    for directory in sorted((root / suite).glob('tee-*')):
        expected = [f'PROBE {index:06d} '.encode().ljust(127, b'x') + b'\n'
                    for index in range(20)]
        if suite == 'sparse-smoke':
            with (root / 'full-final/data/follow_10mib.log').open('rb') as source:
                expected.extend(source.readlines())
        for index in range(4):
            verify_tee((directory / f'log{index}').glob('*.log'), expected)
            count += 1
assert count == 56, count
```

Timed/profiling/trace application runs were serial. The other Codex was idle
during timed runs and had exited before syscall diagnostics; no pause was sent
and no resume is owed. The unrelated pi session was untouched.

The first independent task `3a` reviewer re-ran the 18 Python tests, Bash syntax
and cache checks, recalculated observation counts, receipts, hashes and reported
aggregates, and independently checked the 56 tee files, ten full follow
outputs and 336 scheduled CSVs. The fallback-test issue above was its only
actionable finding.

Fresh follow-up review found no remaining actionable issues. It re-ran all 18
Python tests and both cache-helper branches, checked Bash syntax/whitespace and
recomputed counts, receipts, hash equality, sparse p95 and paced CPU medians.
Both reviewers assessed actual behavior and negative cases rather than just
mock calls. Core oracle, environment and selection helpers are substantially
exercised. Dedicated unit tests do not cover timeout escalation/readiness
retries; actual application runs exercise orchestration. No aggregate coverage
percentage is claimed for the new Python tools; coverage.py and ShellCheck
were unavailable. Neither review reran Go builds or performance workloads.

The final index audit confirmed all 192 curated archive files are staged,
including ignored CSVs, and the unrelated audit document is excluded. The
unqualified staged whitespace check flags raw CSV CRLF endings; allowing CRLF
leaves only the two original `go_env=linux amd64 1 auto ` metadata lines (the
last field is empty). These raw records are intentionally preserved. A separate
staged whitespace check of all authored benchmark source and Markdown passes.
