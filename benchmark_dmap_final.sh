#!/bin/bash

# Benchmark script for dmap turbo mode vs regular mode
set -e

echo "=== DTail dmap Benchmark: Regular vs Turbo Mode ==="
echo "Setting up test environment..."

# Kill any existing servers
pkill -f "dserver.*port (2222|3333)" || true
sleep 1

# Create test data
TEST_DATA="/tmp/dtail_benchmark_data.log"
echo "Creating test data with 100,000 log lines..."
> $TEST_DATA
for i in {1..10000}; do
    for server in server1 server2 server3 server4 server5 server6 server7 server8 server9 server10; do
        echo "2023-12-27 10:00:00 $server component=TestApp level=INFO message=Test_$i goroutines=$((30 + $RANDOM % 20)) connections=$((100 + $RANDOM % 100)) requests=$((1000 + $RANDOM % 1000))" >> $TEST_DATA
    done
done

# Start servers
echo "Starting servers..."
./dserver --cfg none --logLevel error --bindAddress localhost --port 2222 > /tmp/dserver_regular.log 2>&1 &
DTAIL_TURBOBOOST_ENABLE=yes ./dserver --cfg none --logLevel error --bindAddress localhost --port 3333 > /tmp/dserver_turbo.log 2>&1 &
sleep 2

# Query to test
QUERY='select count($server),$server,avg($goroutines),sum($connections),max($requests) from - group by $server order by count($server)'

echo
echo "Running benchmarks..."
echo "Test data: 100,000 lines"
echo "Query: Aggregating by server with multiple operations"
echo

# Regular mode benchmark
echo "=== Regular Mode (port 2222) ==="
time (
    for i in {1..5}; do
        ./dmap -servers localhost:2222 -files "$TEST_DATA" -query "$QUERY" -noColor -plain > /tmp/dmap_regular_$i.out 2>&1
    done
)
REGULAR_LINES=$(wc -l < /tmp/dmap_regular_1.out)
echo "Output lines: $REGULAR_LINES"
echo "Sample output:"
head -3 /tmp/dmap_regular_1.out

echo
echo "=== Turbo Mode (port 3333) ==="
time (
    for i in {1..5}; do
        ./dmap -servers localhost:3333 -files "$TEST_DATA" -query "$QUERY" -noColor -plain > /tmp/dmap_turbo_$i.out 2>&1
    done
)
TURBO_LINES=$(wc -l < /tmp/dmap_turbo_1.out)
echo "Output lines: $TURBO_LINES"
echo "Sample output:"
head -3 /tmp/dmap_turbo_1.out

# Verify outputs match
echo
echo "=== Verification ==="
if diff /tmp/dmap_regular_1.out /tmp/dmap_turbo_1.out > /dev/null; then
    echo "✓ Outputs match!"
else
    echo "✗ Outputs differ!"
    echo "Differences:"
    diff /tmp/dmap_regular_1.out /tmp/dmap_turbo_1.out | head -10
fi

# Cleanup
pkill -f "dserver.*port (2222|3333)" || true

echo
echo "Benchmark complete!"