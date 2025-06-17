#!/bin/bash

# Profile channelless vs channel-based implementations to understand performance difference

set -e

echo "=== Profiling Channelless vs Channel-based Cat Implementation ==="
echo

# Build with profiling enabled
echo "Building DTail binaries..."
make clean > /dev/null 2>&1
make build > /dev/null 2>&1

echo "Profiling channel-based implementation..."
DTAIL_USE_CHANNELLESS=false DTAIL_INTEGRATION_TEST_RUN_MODE=yes \
    go tool pprof -cpuprofile=channel_based_cpu.prof \
    -o channel_based_cpu.prof \
    -- ./dcat --logLevel error --cfg none scripts/test_100mb.txt > /dev/null 2>&1 &
CHANNEL_PID=$!

# Profile with Go's built-in profiling
DTAIL_USE_CHANNELLESS=false DTAIL_INTEGRATION_TEST_RUN_MODE=yes \
    timeout 10s go run -cpuprofile=channel_based_go.prof ./cmd/dcat/main.go --logLevel error --cfg none scripts/test_100mb.txt > /dev/null 2>&1 || true

echo "Profiling channelless implementation..."
DTAIL_USE_CHANNELLESS=true DTAIL_INTEGRATION_TEST_RUN_MODE=yes \
    timeout 10s go run -cpuprofile=channelless_go.prof ./cmd/dcat/main.go --logLevel error --cfg none scripts/test_100mb.txt > /dev/null 2>&1 || true

echo "Analyzing profiles..."

echo
echo "=== Channel-based CPU Profile ==="
if [ -f channel_based_go.prof ]; then
    go tool pprof -top -cum channel_based_go.prof | head -20
else
    echo "Channel-based profile not found"
fi

echo
echo "=== Channelless CPU Profile ==="
if [ -f channelless_go.prof ]; then
    go tool pprof -top -cum channelless_go.prof | head -20
else
    echo "Channelless profile not found"
fi

echo
echo "Profile files generated:"
ls -la *_go.prof 2>/dev/null || echo "No profile files found"