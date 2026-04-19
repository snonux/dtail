# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

DTail (Distributed Tail) is a DevOps tool written in Go for distributed log operations across multiple servers. It provides secure, concurrent access to logs on many machines using SSH protocol, supporting tail, cat, grep, MapReduce operations, and auth-key fast reconnect optimization for repeated SSH connections.

## Build Commands

```bash
# Build all binaries
make build

# Build individual components
make dtail      # Client for tailing log files
make dserver    # Server component (required on target machines)
make dcat       # Client for displaying files
make dgrep      # Client for searching files
make dmap       # Client for MapReduce queries
make dtailhealth # Health check client

# Clean build artifacts
make clean

# Enable ACL support (requires libacl-devel)
DTAIL_USE_ACL=yes make build

# Build without zstd (CGO-free cross-compiles; .zst logs unsupported)
DTAIL_NO_ZSTD=yes make build

# Enable proprietary features
DTAIL_USE_PROPRIETARY=yes make build

# Build PGO-optimized binaries (requires existing profiles)
make build-pgo

# Generate PGO profiles and build optimized binaries
make pgo
```

## Testing & Development

```bash
# Run all tests (unit tests only)
make test

# Run all tests including integration tests
# IMPORTANT: Always rebuild binaries before running integration tests
make clean && make build
DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test

# Quick integration test workflow (recommended)
make build && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test

# Run linting
make lint

# Run go vet
make vet

# Run integration tests individually (requires binaries built first)
cd integrationtests && go test
```

## Benchmarking

```bash
# Run all benchmarks
make benchmark

# Quick benchmarks (subset of tests)
make benchmark-quick

# Full benchmarks with longer runs
make benchmark-full

# Create a baseline for comparison
make benchmark-baseline

# Compare current performance against a baseline
make benchmark-compare BASELINE=benchmarks/baselines/baseline_TIMESTAMP.txt
```

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
```

### PGO Notes

- PGO provides additional performance improvements on top of turbo mode
- Typical improvements: 5-10% for DCat, up to 19% for DGrep with low hit rates
- Profiles are saved in `pgo-profiles/` directory
- Optimized binaries are built in `pgo-build/` directory
- Use `make build-pgo` to rebuild optimized binaries without regenerating profiles
- PGO profiles are workload-specific; consider custom profiles for your use case

## Profiling

```bash
# Profile all commands (dcat, dgrep, dmap)
make profile-all

# Profile individual commands
make profile-dcat         # Profile dcat with test data
make profile-dgrep        # Profile dgrep with test data
make profile-dmap         # Profile dmap MapReduce queries

# Quick profiling with smaller datasets
make profile-quick

# Full automated profiling (includes larger files)
make profile-auto

# Clean all profile data
make profile-clean

# Analyze a specific profile interactively
make profile-analyze PROFILE=profiles/dcat_cpu_*.prof

# Generate flame graph visualization
make profile-flamegraph PROFILE=profiles/dcat_cpu_*.prof

# Custom profiling options
PROFILE_SIZE=10000000 make profile-all    # Profile with 10M lines
PROFILE_DIR=myprofiles make profile-dcat  # Custom profile directory

# Show all profiling options
make profile-help
```

### Profiling Notes

- Profiles are saved in the `profiles/` directory by default
- Each command generates CPU, memory, and allocation profiles
- Use `go tool pprof` for detailed analysis of profile files

## Test Execution Details

- Integration tests require binaries to be built before execution
- **IMPORTANT:** Always recompile binaries after code changes before running integration tests:
  ```bash
  make clean && make build
  DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test
  ```
- Integration tests are run by setting DTAIL_INTEGRATION_TEST_RUN_MODE to yes, and by running 'make test'
- Integration tests verify: DCat, DGrep, DMap (MapReduce), DServer, DTail, DTailHealth, and auth-key fast reconnect functionality
- All tests run with race detection enabled (`--race` flag)

## Known Limitations

### Interactive Query Reload
Interactive query control is opt-in on the client with `--interactive-query`.
The controlling TTY accepts `:reload <flags>`, `:show`, `:help`, and `:quit`.

**Compatibility and session semantics:**
- Initial interactive bootstrap prefers `SESSION START` when the server
  advertises capability `query-update-v1`
- If that capability is absent, startup falls back to the legacy command stream
  automatically so mixed-version client/server combinations still run
- Live `:reload` updates require every active server connection to advertise
  `query-update-v1`; unsupported servers cause the reload to be rejected while
  the current workload keeps running
- Successful reloads reuse the existing SSH session and advance a generation
  boundary so stale output from older workloads is dropped

### Auth-Key Fast Reconnect
Auth-key fast reconnect is enabled by default. The client can register a public key with `dserver` over an already-authenticated session, and subsequent connections can use this in-memory key before falling back to normal SSH auth.

**Technical Details:**
- Client sends `AUTHKEY <base64-pubkey>` command during session setup
- Server stores keys in memory, per user, with TTL and max-keys limits
- SSH `PublicKeyCallback` checks in-memory auth-key store before `authorized_keys`
- If fast-path auth misses (restart/expiry/mismatch), normal SSH auth is used automatically

**Config and Flags:**
- Client flag: `--auth-key-path` (default `~/.ssh/id_rsa`)
- Client flag: `--no-auth-key` (disable feature)
- Client env: `DTAIL_AUTH_KEY_PATH` (primary env alias for auth key path; takes precedence over `DTAIL_SSH_PRIVATE_KEYFILE_PATH`)
- Client env: `DTAIL_SSH_PRIVATE_KEYFILE_PATH` (legacy alias; used only when `DTAIL_AUTH_KEY_PATH` is not set)
- Env var precedence (highest to lowest): CLI flag → `DTAIL_AUTH_KEY_PATH` → `DTAIL_SSH_PRIVATE_KEYFILE_PATH`
- Server config: `AuthKeyEnabled` (default `true`)
- Server config: `AuthKeyTTLSeconds` (default `86400`)
- Server config: `AuthKeyMaxPerUser` (default `5`)

### Turbo Mode and MapReduce Operations
Turbo boost mode is enabled by default and provides performance optimizations for both direct output operations (cat, grep, tail) and MapReduce operations when running in server mode. It can be explicitly disabled via DTAIL_TURBOBOOST_DISABLE=yes or TurboBoostDisable in the config file.

**Technical Details:**
- For cat/grep/tail: Turbo mode bypasses channels for direct writing
- For MapReduce in server mode: Turbo mode uses direct line processing without channels
- For MapReduce in serverless/client mode: Turbo mode is not applicable (client-side aggregation doesn't use server optimizations)

**Server-Side Turbo MapReduce:**
When turbo mode is enabled and using dserver:
- Lines are processed directly without channel overhead
- Batch processing reduces lock contention
- Memory pooling reduces garbage collection pressure
- Same output format and accuracy as regular MapReduce

**High-Concurrency MapReduce Improvements:**
- Channel recycling ensures proper draining before reuse (regular mode)
- Buffer sizes increased from 1,000 to 10,000 for better concurrency
- Direct processing in turbo mode eliminates channel bottlenecks
- Improved synchronization in both regular and turbo aggregate implementations

**Best Practices for High-Concurrency MapReduce:**
1. Turbo boost is enabled by default. To disable if needed: `export DTAIL_TURBOBOOST_DISABLE=yes`
2. Increase MaxConcurrentCats in the server configuration to match workload
3. Use server mode for large-scale MapReduce operations
4. Monitor logs for performance metrics

## Benchmarking & Profiling

```bash
# Run benchmarks
make benchmark

# Run performance profiling
make profile

# Generate profiling reports
make profile-report

# Run specific benchmark suites
make benchmark-network
make benchmark-mapreduce
make benchmark-ssh
```

## Profile-Guided Optimization (PGO)

```bash
# Run PGO for all commands
make pgo

# Quick PGO with smaller datasets
make pgo-quick

# PGO for specific commands
make pgo-commands COMMANDS='dcat dgrep'

# Clean PGO artifacts
make pgo-clean

# Show PGO help
make pgo-help

# Direct usage with dtail-tools
dtail-tools pgo                    # Optimize all commands
dtail-tools pgo dcat dgrep         # Optimize specific commands
dtail-tools pgo -v -iterations 5   # Verbose with 5 iterations

# After PGO, optimized binaries are in pgo-build/
```

### PGO Notes

- PGO uses profile data from real workloads to optimize binary performance
- The process involves: building baseline → generating profiles → building with PGO
- Typical improvements range from 5-20% depending on the workload
- Optimized binaries are placed in the `pgo-build/` directory

## Architecture & Code Organization

### Binary Entry Points
- `/cmd/dtail/` - Remote log tailing client
- `/cmd/dserver/` - Server daemon
- `/cmd/dcat/` - Remote file reading client
- `/cmd/dgrep/` - Remote file searching client
- `/cmd/dmap/` - MapReduce query client
- `/cmd/dtailhealth/` - Health check client

### Core Implementation
- `/internal/clients/` - Client implementations for each tool
- `/internal/server/` - Server daemon logic
- `/internal/mapr/` - MapReduce engine and query parsing
- `/internal/ssh/` - SSH client/server components
- `/internal/config/` - Configuration management
- `/internal/io/` - File operations, logging, compression handling

### Key Architectural Patterns

1. **Client-Server Communication**: All clients communicate with dserver instances via SSH protocol on port 2222 (configurable)

2. **MapReduce Query Engine**: Located in `/internal/mapr/`, implements SQL-like query language for distributed log aggregation

3. **Configuration System**: JSON-based configuration in `/internal/config/`, supports both client and server settings

4. **SSH Integration**: Custom SSH server implementation in `/internal/ssh/server/` and client in `/internal/ssh/client/`

5. **Compression Support**: Automatic handling of gzip and zstd compressed files in `/internal/io/`

6. **Auth-Key Fast Reconnect**: Client registers a public key via `AUTHKEY`; server validates against in-memory auth-key cache before falling back to `authorized_keys`

## Important Implementation Details

- **Main Server Loop**: `/internal/server/server.go` - Core server processing logic
- **Client Base**: `/internal/clients/baseClient.go` - Common client functionality
- **MapReduce Parser**: `/internal/mapr/parse/` - SQL-like query language parser
- **Log Format Parsers**: `/internal/mapr/logformat/` - Extensible log parsing system
- **SSH Authorization Callback**: `/internal/ssh/server/publickeycallback.go` - auth-key fast-path + `authorized_keys` fallback
- **Auth-Key Cache**: `/internal/ssh/server/authkeystore.go` - in-memory per-user key cache (TTL/max-keys)
- **AUTHKEY Handler**: `/internal/server/handlers/serverhandler.go` - session command handling for auth-key registration

## Configuration Files

- Server config: `/etc/dserver/dtail.json` or `./dtail.json`
- Example configs: `/examples/`
- Docker configs: `/docker/`

### Auth-Key Related Options

- Client: `--auth-key-path`, `--no-auth-key`
- Client config: `Client.AuthKeyPath`, `Client.AuthKeyDisable`
- Client env: `DTAIL_AUTH_KEY_PATH` (takes precedence over `DTAIL_SSH_PRIVATE_KEYFILE_PATH`)
- Client env: `DTAIL_SSH_PRIVATE_KEYFILE_PATH` (legacy; used only when `DTAIL_AUTH_KEY_PATH` is unset)
- Server config: `Server.AuthKeyEnabled`, `Server.AuthKeyTTLSeconds`, `Server.AuthKeyMaxPerUser`

## Common Development Tasks

When modifying client behavior:
1. Check `/internal/clients/` for the specific client implementation
2. Common functionality is in `baseClient.go`
3. Client-specific logic is in respective files (e.g., `tail.go`, `cat.go`)

When modifying server behavior:
1. Core server logic is in `/internal/server/server.go`
2. User authentication in `/internal/server/user/`
3. Handler implementations in `/internal/server/handlers/`

When working with MapReduce:
1. Query parsing in `/internal/mapr/parse/`
2. Aggregation logic in `/internal/mapr/reducer/`
3. Log format parsing in `/internal/mapr/logformat/`
