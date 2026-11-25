# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

DTail (Distributed Tail) is a DevOps tool written in Go for distributed log operations across multiple servers. It provides secure, concurrent access to logs on many machines using SSH protocol, supporting tail, cat, grep, MapReduce operations, and auth-key fast reconnect optimization for repeated SSH connections.

## Build Commands

```bash
# Build all binaries (also the default target, `make` / `make all`)
make build

# Build individual components
make dtail      # Client for tailing log files
make dserver    # Server component (required on target machines)
make dcat       # Client for displaying files
make dgrep      # Client for searching files
make dmap       # Client for MapReduce queries
make dtailhealth # Health check client
make dtail-tools # Developer toolkit: benchmark, profile and pgo subcommands

# go install the six product binaries (not dtail-tools) into $GOBIN
make install

# Remove the binaries of every ./cmd/* and all *.tmp and *.prof files in the tree
make clean

# Enable ACL support (build tag linuxacl, cgo with -lacl; requires libacl-devel)
DTAIL_USE_ACL=yes make build

# Build without zstd (build tag nozstd; CGO-free cross-compiles; .zst logs unsupported)
DTAIL_NO_ZSTD=yes make build

# Enable proprietary features (build tag proprietary). Needs the proprietary
# mimecast log format source, which is not in this repository: without it the
# build fails with "undefined: newMimecastParser".
DTAIL_USE_PROPRIETARY=yes make build

# Build PGO-optimized binaries (requires existing profiles)
make build-pgo

# Generate PGO profiles and build optimized binaries
make pgo
```

The three `DTAIL_*` build switches are tested with make's `ifdef`, so any
non-empty value (also `no`) enables them. The tags apply to every build, test,
vet and lint target (`GO_TAGS`); `GO_FLAGS` is passed to the binary builds.

## Testing & Development

```bash
# Run all tests (unit tests only; the integration tests skip themselves).
# Runs `go clean -testcache`, then `go test -race -shuffle=on -timeout 30m ./...`
make test

# Run all tests including integration tests
# IMPORTANT: Always rebuild binaries before running integration tests
make clean && make build
DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test

# Quick integration test workflow (recommended)
make build && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test

# Integration tests only: clean, build, then
# DTAIL_INTEGRATION_TEST_RUN_MODE=yes go test -v --race -count=1 ./integrationtests
make test-integration

# Run linting (golangci-lint; override the binary with GOLANGCI_LINT=...)
make lint

# Run go vet
make vet

# List NEXT:/TODO: markers in the tree
make todos

# Re-render the documentation GIFs (doc/*.gif) from doc/tapes/*.tape against a
# demo fleet of six local dservers (needs vhs with ttyd and ffmpeg, gifsicle)
make gifs

# Run integration tests individually (requires binaries built first; without
# the variable every integration test is skipped)
cd integrationtests && DTAIL_INTEGRATION_TEST_RUN_MODE=yes go test -race -count=1 -run 'TestDCat'
```

## Benchmarking

```bash
# Run all benchmarks
make benchmark

# Quick benchmarks (subset of tests)
make benchmark-quick

# Full benchmarks with longer runs
make benchmark-full

# Create a baseline for comparison (prompts for a name)
make benchmark-baseline

# Create a quick baseline (small files only, prompts for a name)
make benchmark-baseline-quick

# Compare current performance against a baseline (without BASELINE it lists them)
make benchmark-compare BASELINE=benchmarks/baselines/baseline_TIMESTAMP.txt

# Compare the local fork with upstream (checkout of github.com/mimecast/dtail at
# ../dtail-upstream; other paths and options only by running
# benchmarks/upstream_vs_local_bench.sh smoke|run --upstream-root PATH ... directly)
make benchmark-upstream-smoke   # Compile both trees, tiny correctness checks, no timings
make benchmark-upstream         # Paired end-to-end scenarios; run only on an idle host

# Drop system caches before a benchmark run (sudo; /usr/local/sbin/drop-caches
# if executable, else benchmarks/drop_caches.sh)
make drop-caches
```

The benchmark targets build first and run `dtail-tools benchmark`, a wrapper
around `go test -bench ./benchmarks`: `-mode run|baseline|compare|list|clean`,
`-quick` (`-bench=BenchmarkQuick`), `-memory` (`-benchmem`), `-iterations 3x`
(`-benchtime`, used by `benchmark-full`), `-tag NAME`, `-baseline FILE`,
`-output FILE`, `-verbose`, `-dir` (baselines, default `benchmarks/baselines`).

## Profile-Guided Optimization (PGO)

```bash
# Full PGO workflow: generate profiles and build optimized binaries
make pgo

# Quick PGO with smaller datasets (faster)
make pgo-quick

# PGO for specific commands only
make pgo-commands COMMANDS='dcat dgrep'

# Generate PGO profiles only (without building)
make pgo-generate

# Build PGO-optimized binaries using existing profiles
make build-pgo

# Install PGO-optimized binaries to system
make install-pgo

# Clean PGO artifacts
make pgo-clean

# Show PGO help
make pgo-help

# Direct usage with dtail-tools (run from the repository root)
dtail-tools pgo                    # Optimize all commands
dtail-tools pgo dcat dgrep         # Optimize specific commands
dtail-tools pgo -v -iterations 5   # Verbose with 5 iterations
dtail-tools pgo -profileonly       # Profiles only (what make pgo-generate runs)
```

## Profiling

```bash
# Profile all commands (dcat, dgrep, dmap)
make profile-all

# Quick profiling with smaller datasets
make profile-quick

# Profile dmap MapReduce queries
make profile-dmap

# List available profiles
make profile-list

# Analyze a specific profile interactively
make profile-analyze PROFILE=profiles/dcat_cpu_*.prof

# Open the web interface for a profile (go tool pprof -web)
make profile-web PROFILE=profiles/dcat_cpu_*.prof

# Clean all profile data (removes the profiles/ and testdata/ directories)
make profile-clean

# Show all profiling options
make profile-help
```

## Topic Notes (read before changing that area)

AGENTS.md holds only what every task needs. The details of each area are in
`docs/agents/`: read the file of the area you change before changing it, and
update it in the same change. Sections named in quotes inside those files
(see "Wire Framing") are the topics of this table.

| Topic | File | Read it when you touch |
| --- | --- | --- |
| Interactive Query Reload | [interactive-query.md](docs/agents/interactive-query.md) | the interactive default, control commands, line editor (paste, vi mode, external editor), Tab completion, colors, the output pause |
| "Tab waits hold the output" | [interactive-output-hold.md](docs/agents/interactive-output-hold.md) | `interactivePause.holdOutput`, `outputHold`, the drain after a pause |
| "Compatibility and session semantics" | [interactive-sessions.md](docs/agents/interactive-sessions.md) | `SESSION START`/`UPDATE`, ack waits and the ack gate, `:reload` across connections, generations, `dtail` switching between lines and a query |
| MapReduce client reports | [mapreduce-client-reports.md](docs/agents/mapreduce-client-reports.md) | `outfile append`, interim report timing, logging under the MapReduce state's locks |
| JSON control channel | [control-json.md](docs/agents/control-json.md) | `--control json`, the request loop, its commands, query runs and result events, the `fdata` logger |
| dmap Query Builder | [dmap-query-builder.md](docs/agents/dmap-query-builder.md) | `dmap` without a query, held sessions, paste handling, builder history |
| Auth-Key Fast Reconnect | [auth-key.md](docs/agents/auth-key.md) | `AUTHKEY`, the in-memory key store, its flags, env variables and config |
| Host Key Verification | [host-keys.md](docs/agents/host-keys.md) | `known_hosts`, `--trustAllHosts`, connect and handshake timeouts |
| File Paths With Spaces | [file-paths.md](docs/agents/file-paths.md) | read command paths, `base64%` encoding, commas and newlines in paths |
| Journal Source Reads | [journal.md](docs/agents/journal.md) | `journal:` targets, `journal-v1`, unit name validation |
| Probe Command | [probe.md](docs/agents/probe.md) | field discovery, table probes, path listing, the probe sampler |
| Read Progress | [read-progress.md](docs/agents/read-progress.md) | `.syn progress`, `--progress`, the STATS progress fields |
| Wire Framing | [wire-framing.md](docs/agents/wire-framing.md) | frames, escaping, control frame markers, capabilities, protocol version |
| Diagnostic Escaping | [diagnostic-escaping.md](docs/agents/diagnostic-escaping.md) | what log details are escaped |
| Client Log Contents | [client-logs.md](docs/agents/client-logs.md) | `fout`, `--log-payload`, logger and log directory precedence, serverless output |
| Client Exit Statuses | [exit-statuses.md](docs/agents/exit-statuses.md) | exit statuses of `dcat`/`dgrep`, failed commands, sessions cut short, protocol mismatch |
| Output Path and MapReduce Operations | [output-path.md](docs/agents/output-path.md) | the read/output path, server-side MapReduce, output limits and deadlines, idle timeout |
| Shared reads | [shared-reads.md](docs/agents/shared-reads.md) | the read hub, shared follow reads, eviction and rejoin, scheduled job group reads |
| Failed scheduled jobs | [scheduled-jobs.md](docs/agents/scheduled-jobs.md) | scheduler outfiles, backoff, final runs, transport vs file read failures |
| PGO and profiling notes | [pgo-profiling.md](docs/agents/pgo-profiling.md) | `dtail-tools pgo`/`profile`, their flags and limits |
| Test execution details | [testing.md](docs/agents/testing.md) | integration test mode, the interactive PTY harness |

Design documents for developers are in `docs/dev/` (`probe.md`,
`wire_framing.md`, `read_progress.md`, `profiling.md`); user documentation is
in `doc/`.

## Architecture & Code Organization

### Binary Entry Points
- `/cmd/dtail/` - Remote log tailing client
- `/cmd/dserver/` - Server daemon
- `/cmd/dcat/` - Remote file reading client
- `/cmd/dgrep/` - Remote file searching client
- `/cmd/dmap/` - MapReduce query client
- `/cmd/dtailhealth/` - Health check client
- `/cmd/dtail-tools/` - Developer toolkit (`benchmark`, `profile`, `pgo`; not installed by `make install`)

### Core Implementation
- `/internal/clients/` - Client implementations for each tool
- `/internal/cli/` - Shared client flag binding, runtime lifecycle, interactive defaults, pprof startup
- `/internal/server/` - Server daemon logic
- `/internal/handlers/` - Transport-neutral session handlers and read engine
- `/internal/jobs/` - Scheduled and continuous MapReduce client workloads
- `/internal/mapr/` - MapReduce engine and query parsing
- `/internal/protocol/` - Wire version, framing/escaping, control frames, probe and session wire types
- `/internal/session/` - Workload specs: plain command streams and `SESSION START`/`UPDATE`
- `/internal/authkey/` - Shared in-memory cache for registered public keys
- `/internal/sessionuser/` - Session identity and read authorization
- `/internal/ssh/` - SSH client/server components
- `/internal/config/` - Configuration management
- `/internal/io/` - File operations (`fs`, `fs/readhub`), journal reads, logging (`dlog`), line processing, pools, prompts, signals
- `/internal/discovery/` - Server list resolution (comma lists, files, modules)
- `/internal/color/`, `/internal/logging/` - Terminal colors, logger interface and messages
- `/internal/profiling/` - `-profile`/`-cpuprofile`/`-memprofile` capture for the clients
- `/internal/tools/` - `dtail-tools` subcommands (`benchmark`, `profile`, `pgo`, shared `common`)
- Small helpers: `ctxutil`, `lcontext` (grep context), `omode` (operation modes), `options` (read option map), `regex`, `source`, `user` (no running as root), `version`, and `done.go` (shutdown signal)

### Key Architectural Patterns

1. **Client-Server Communication**: All clients communicate with dserver instances via SSH protocol on port 2222 (configurable)

2. **MapReduce Query Engine**: Located in `/internal/mapr/`, implements SQL-like query language for distributed log aggregation

3. **Configuration System**: JSON-based configuration in `/internal/config/`, supports both client and server settings

4. **SSH Integration**: Custom SSH server implementation in `/internal/ssh/server/` and client in `/internal/ssh/client/`

5. **Compression Support**: Automatic handling of gzip and zstd compressed files in `/internal/io/`

6. **Auth-Key Fast Reconnect**: Client registers a public key via `AUTHKEY`; server validates against in-memory auth-key cache before falling back to `authorized_keys`

## Important Implementation Details

- **Main Server Loop**: `/internal/server/server.go` - Core server processing logic
- **Client Base**: `/internal/clients/baseclient.go` - Common client functionality
- **MapReduce Parser**: `/internal/mapr/` (query.go, token.go, whereclause.go, wherecondition.go, selectcondition.go, groupset.go, etc.) - SQL-like query language parser
- **Partial Query Analysis**: `/internal/mapr/complete.go` (`AnalyzePartial`, with complete_scan.go and complete_walk.go) - reads a query typed up to the cursor with the tokenizer and clause rules of the parser: token kinds with byte offsets for highlighting, the word under the cursor, whether a comma precedes it (`AfterComma`) and the kinds of words expected there for completion; `AnalyzeQuery` reads a whole query (both sides of the cursor) the same way, with no word under the cursor, for the clauses and table a completion must know; tests compare its tokens with `tokenize` so they cannot drift
- **Log Format Parsers**: `/internal/mapr/logformat/` - Extensible log parsing system
- **Field Probe Sampler**: `probe.Sample` (`/internal/mapr/probe/sample.go`); see [probe.md](docs/agents/probe.md)
- **SSH Authorization Callback**: `/internal/ssh/server/publickeycallback.go` - auth-key fast-path + `authorized_keys` fallback
- **AUTHKEY Handler**: `/internal/handlers/serverhandler.go` - session command handling for auth-key registration
- **Auth-Key Cache**: `/internal/authkey/authkeystore.go` - in-memory per-user key cache (TTL/max-keys)

## Configuration Files

- Config file: `--cfg PATH` (every binary; `--cfg none` loads none). Without
  `--cfg`, the first of `~/.config/dtail/dtail.conf` and `~/.dtail.conf` that
  exists is read (not merged; `config.initializer.parseConfig`). There is no
  `./dtail.json` lookup: dserver reads `/etc/dserver/dtail.json` only because
  the example systemd unit passes `-cfg /etc/dserver/dtail.json`
- Example configs: `/examples/` (`dtail.json.example`, the JSON schema
  `dtail.schema.json`, systemd units and helper scripts)
- Docker configs: `/docker/`

## Common Development Tasks

When modifying client behavior:
1. Check `/internal/clients/` for the specific client implementation
2. Common functionality is in `baseclient.go`
3. Client-specific logic is in respective files (e.g., `tailclient.go`, `catclient.go`)

When modifying server behavior:
1. Core server logic is in `/internal/server/server.go`
2. Session identity and read authorization in `/internal/sessionuser/`
3. Handler implementations in `/internal/handlers/`

When working with MapReduce:
1. Query parsing in `/internal/mapr/` (query.go, token.go, whereclause.go, wherecondition.go, selectcondition.go, groupset.go, etc.)
2. In-process aggregation in `/internal/mapr/aggregate/`
3. Log format parsing in `/internal/mapr/logformat/`
4. Log format probes (`logformat.Probe`, `ProbeWithParser`, the optional `Prober`, `Names`, `KnownVariables`) in `/internal/mapr/logformat/probe.go`; see "Custom probes" in `doc/logformats.md`
5. The language vocabulary is table-driven: clause keywords (`keywords`,
   `mapr.Keywords`), aggregations (`aggregationTable`,
   `mapr.AggregationNames`), where operators (`whereOperatorTable`,
   `mapr.WhereOperators`, with a numeric/string kind) and set functions
   (`funcs.functionTable`, `funcs.Names`). The parser and the exported
   accessors (for query completion) read the same tables, resolved at parse
   time only; add a new word to its table, not to a switch. The table
   only names words: a new spelling of an existing operation (e.g. `=<`)
   needs nothing else, and a set function brings its implementation in its
   table entry, but new behaviour still needs code beyond the table: a
   clause keyword its clause in the parser (`query.go`), a where operator
   its `QueryOperation` evaluated in `whereCondition.floatClause` or
   `stringClause`, and an aggregation its `AggregateOperation` handled in
   `AggregateSet.Aggregate` and `fieldMergerFor` (`aggregateset.go`) and in
   the result rendering (`GroupSet.resultSelect`, `groupset.go`).
   `TestVocabularyMatchesDoc` fails when `doc/querylanguage.md` differs.
