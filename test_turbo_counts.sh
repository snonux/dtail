#!/bin/bash

# Test turbo mode line counting
set -e

echo "=== Testing Turbo Mode Line Processing ==="

# Kill any existing servers
pkill -f "dserver.*port (6666|7777)" || true
sleep 1

# Create test data
TEST_DATA="/tmp/turbo_test_data.log"
echo "Creating test data with exactly 1000 lines..."
> $TEST_DATA
for i in {1..1000}; do
    echo "2023-12-27 10:00:$((i % 60)) server1 component=TestApp level=INFO message=Test_$i goroutines=30" >> $TEST_DATA
done
echo "Test data lines: $(wc -l < $TEST_DATA)"

# Start servers
echo "Starting servers..."
./dserver --cfg none --logLevel debug --bindAddress localhost --port 6666 > /tmp/dserver_regular.log 2>&1 &
DTAIL_TURBOBOOST_ENABLE=yes ./dserver --cfg none --logLevel debug --bindAddress localhost --port 7777 > /tmp/dserver_turbo.log 2>&1 &
sleep 2

# Simple query to count lines
QUERY='select count($server),$server from - group by $server'

echo
echo "=== Regular Mode (port 6666) ==="
./dmap -servers localhost:6666 -files "$TEST_DATA" -query "$QUERY" -noColor -plain -trustAllHosts 2>&1 | tee /tmp/regular_output.txt

echo
echo "=== Turbo Mode (port 7777) ==="
./dmap -servers localhost:7777 -files "$TEST_DATA" -query "$QUERY" -noColor -plain -trustAllHosts 2>&1 | tee /tmp/turbo_output.txt

echo
echo "=== Server Logs ==="
echo "Regular mode processing:"
grep -E "(Processing batch|Batch processed|lines processed)" /tmp/dserver_regular.log | tail -10 || true

echo
echo "Turbo mode processing:"
grep -E "(Processing batch|Batch processed|lines processed|TurboAggregate)" /tmp/dserver_turbo.log | tail -20 || true

# Cleanup
pkill -f "dserver.*port (6666|7777)" || true