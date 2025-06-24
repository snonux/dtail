# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

DTail (Distributed Tail) is a DevOps tool written in Go for distributed log operations across multiple servers. It provides secure, concurrent access to logs on many machines using SSH protocol, supporting tail, cat, grep, and MapReduce operations.

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

# Install binaries
make install

# Clean build artifacts
make clean

# Enable ACL support (requires libacl-devel)
DTAIL_USE_ACL=yes make build

# Enable proprietary features
DTAIL_USE_PROPRIETARY=yes make build
```

## Testing & Development

```bash
# Run all tests (unit tests only)
make test

# Run all tests including integration tests
DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test

# Run linting
make lint

# Run go vet
make vet

# Run integration tests individually (requires binaries built)
cd integrationtests && go test
```

## Test Execution Details

- Integration tests are run by setting DTAIL_INTEGRATION_TEST_RUN_MODE to yes, and by running 'make test'.

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

## Important Implementation Details

- **Main Server Loop**: `/internal/server/server.go` - Core server processing logic
- **Client Base**: `/internal/clients/baseClient.go` - Common client functionality
- **MapReduce Parser**: `/internal/mapr/parse/` - SQL-like query language parser
- **Log Format Parsers**: `/internal/mapr/logformat/` - Extensible log parsing system
- **SSH Authorization**: `/internal/server/user/authsshkey.go` - SSH key validation

## Configuration Files

- Server config: `/etc/dserver/dtail.json` or `./dtail.json`
- Example configs: `/examples/`
- Docker configs: `/docker/`

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