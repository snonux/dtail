# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Development Commands

### Building
```bash
# Build all binaries
make clean; make build

# Build individual binaries
make dserver    # Server daemon
make dtail      # Main client
make dcat       # Distributed cat
make dgrep      # Distributed grep  
make dmap       # MapReduce client
make dtailhealth # Health check utility
```

### Testing
```bash
# Run all unit tests
make test

# Run integration tests (requires environment variable)
DTAIL_INTEGRATION_TEST_RUN_MODE=yes go test -v ./integrationtests/

# IMPORTANT: Always recompile binaries before running integration tests
make clean build && DTAIL_INTEGRATION_TEST_RUN_MODE=yes go test -v ./integrationtests/

# Clean test cache before running
go clean -testcache
```

Before deciding work is done, ensure that all tests pass (including integration tests) and that the code is well-documented. Before testing, always rebuild all binaries.

## Important Development Guidelines

- **Think Hard**: Always analyze problems thoroughly before implementing solutions. Consider edge cases, performance implications, and how changes affect the entire system.
- **Test All Changes**: Every code change must be tested with both unit tests and integration tests. Build and test immediately after making changes to catch issues early.

### Code Quality
```bash
# Run go vet on all packages
make vet

# Run linting
make lint
```

### Performance Optimization
```bash
# Run Performance Guided Optimization (PGO) for dgrep
make pgo

# This implements true PGO using Go's -pgo compiler flag:
# - Create test file (100MB with 1M lines) in scripts/ if needed
# - Build baseline version without PGO
# - Collect CPU profile for training data
# - Rebuild dgrep with -pgo flag using the training profile
# - Compare baseline vs PGO-optimized performance (5 iterations each)
# - Generate detailed before/after analysis report (scripts/pgo_report.txt)
# 
# All PGO files are organized in scripts/ directory to keep project root clean
```

### Installation
```bash
# Install all binaries to $GOPATH/bin
make install
```

### Optional Build Tags
- `DTAIL_USE_ACL=1` - Enable Linux ACL support  
- `DTAIL_USE_PROPRIETARY=1` - Enable proprietary features

## Architecture Overview

DTail is a distributed log processing system with client-server architecture using SSH for secure communication.

### Core Binaries
- **dtail** - Main client for distributed log tailing and MapReduce
- **dserver** - SSH-based server daemon (runs on port 2222)
- **dcat/dgrep/dmap/dtailhealth** - Specialized distributed tools

### Key Internal Components

#### Client Architecture (`internal/clients/`)
- All clients inherit from `baseClient` with SSH connection management
- Client types: TailClient, CatClient, GrepClient, MaprClient, HealthClient
- Connector pattern: ServerConnection (SSH) vs Serverless (local)
- Handlers manage protocol communication

#### Server Architecture (`internal/server/`) 
- SSH server with multi-user support and resource management
- Handler system routes requests to appropriate processors
- Background services for scheduled jobs and continuous monitoring

#### Protocol (`internal/protocol/`)
- Binary message protocol with specific delimiters:
  - `¬` for message separation
  - `|` for field separation
  - `≔` for key-value pairs in aggregations

#### MapReduce System (`internal/mapr/`)
- SQL-like query syntax: `SELECT...FROM...WHERE...GROUP BY`
- Server-side local aggregation, client-side final aggregation
- Pluggable log format parsers in `internal/mapr/logformat/`

#### Configuration (`internal/config/`)
- Hierarchical: Common, Server, Client configurations
- Supports config files, environment variables, and CLI args

#### Discovery (`internal/discovery/`)
- Pluggable server discovery using reflection
- Built-in: comma-separated, file-based, regex filtering

## Development Patterns

### Resource Management
- Channel-based coordination and goroutine lifecycle management
- Connection throttling using configurable limits per CPU core
- Object recycling and buffer pools for high-throughput scenarios

### Security Model
- SSH-first architecture with encrypted communication
- User-based access control with different privilege levels
- Configurable host key verification policies

### Error Handling
- Structured logging with multiple levels and output targets
- Graceful degradation when servers are unavailable
- Context-aware operations using Go's context package

## Integration Testing Guidelines

- Integration tests for serverless and server mode should always rely on exact the same test files. Same count, same content, same sizes. No exceptions.