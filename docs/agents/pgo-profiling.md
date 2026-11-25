# PGO and Profiling Notes

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

## PGO

- `dtail-tools pgo` flags: `-profiledir` (default `pgo-profiles`), `-outdir`
  (default `pgo-build`), `-datasize` (log lines, default 1000000; the csv gets
  a tenth), `-iterations` (default 3, merged with `go tool pprof -proto`),
  `-v`/`-verbose`, `-profileonly`. `make pgo-quick` is `-datasize 100000
  -iterations 2`. The workflow: baseline builds (`pgo-build/<cmd>-baseline`)
  -> profiles -> PGO builds (`pgo-build/<cmd>`) -> a timing comparison.
- The comparison step (`comparePerformance`, `benchmarkCommand`) knows only
  dcat, dgrep and dmap: for dtail and dserver it returns `unknown benchmark
  command`, so a run that includes them (the default set) ends with an error
  after the profiles and the PGO binaries were written.
- PGO provides additional performance improvements on top of DTail's default optimized read/output path
- Measured improvements are workload-dependent and modest: on a 100 MB
  serverless run, DGrep ~7-8%, DCat ~3%, DMap within measurement noise. PGO
  output is byte-identical to non-PGO output (verified with `cmp`).
- Profiles are saved in `pgo-profiles/` directory
- `pgo-profiles/` and `pgo-build/` are **gitignored**: profiles are regenerated
  locally with `make pgo` / `make pgo-generate`, not committed. They are
  workload-specific; regenerate them after significant hot-path changes.
- Optimized binaries are built in `pgo-build/` directory
- Use `make build-pgo` to rebuild optimized binaries without regenerating profiles
- The tooling (`verifyProfileNonEmpty` in `internal/tools/pgo`) fails loudly
  on missing, 0-byte, or zero-sample profiles, so an idle-server or
  I/O-bound capture can no longer silently produce an empty/zero-sample profile.
- **All five commands are profiled by default.** The `dtail-tools pgo` default
  command set covers dcat, dgrep, dmap, dtail and dserver. dtail used to be
  excluded because its follow client never returned from `client.Start` under
  `-shutdownAfter`, SIGINT or SIGTERM (the "auto shutdown does not work" bug
  noted in `cmd/dtail/main.go`), producing a 0-byte `dtail.pprof`; that follow
  shutdown is now fixed. PGO profiles dtail with a dedicated live-server
  follow workload (`runDtailWorkload` in `internal/tools/pgo`: live dserver,
  deterministic keypair auth, `-shutdownAfter 6`, plus a handshake-failure
  representativeness guard). The separate `internal/tools/profile` profiling
  harness still covers dcat, dgrep and dmap only.

## Profiling

- Profiles are saved in the `profiles/` directory by default
- The targets run `dtail-tools profile -mode quick|full|dmap|list|analyze`
  (flags `-dir`, `-testdata`, `-runs`, `-commands dcat,dgrep`, `-timeout`,
  `-nocolor`; `analyze FILE [-web]`); generated test data goes to `testdata/`
- The clients (`dcat`, `dgrep`, `dmap`, `dtail`) take `-cpuprofile`,
  `-memprofile`, `-profile` (both) and `-profiledir DIR`; with `-profile` each
  run writes `<cmd>_cpu_<ts>.prof`, `<cmd>_mem_<ts>.prof` and
  `<cmd>_alloc_<ts>.prof`. `dserver` and `dtailhealth` have only
  `--pprof ADDR` (an HTTP pprof endpoint); dserver also enables mutex and
  block profiling with it
- Use `go tool pprof` for detailed analysis of profile files; see
  `docs/dev/profiling.md`
