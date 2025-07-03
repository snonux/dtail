#!/bin/bash

# Debug turbo mode test
set -e

echo "=== Debug Turbo Mode Test ==="

# Kill any existing servers
pkill -f "dserver.*port 8888" || true
sleep 1

# Create simple test data
TEST_DATA="/tmp/debug_test.log"
echo "Creating test data..."
> $TEST_DATA
# Create exactly 10 lines with timestamp 1002-071808
for i in {1..10}; do
    echo "INFO|1002-071808|1|stats.go:56|8|11|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1" >> $TEST_DATA
done
# Add some other lines
for i in {1..10}; do
    echo "INFO|1002-071900|1|stats.go:56|8|12|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1" >> $TEST_DATA
done

echo "Test data created: $(wc -l < $TEST_DATA) lines"
echo "Lines with 1002-071808: $(grep -c "1002-071808" $TEST_DATA)"

# Start server with turbo mode
echo "Starting turbo server..."
DTAIL_TURBOBOOST_ENABLE=yes ./dserver --cfg integrationtests/test_server_100files.json --logLevel trace --bindAddress localhost --port 8888 > /tmp/turbo_debug.log 2>&1 &
sleep 2

# Run simple query
QUERY='from STATS select count($time),$time from - group by $time'

echo "Running dmap query..."
./dmap -servers localhost:8888 -files "$TEST_DATA,$TEST_DATA,$TEST_DATA" -query "$QUERY" -noColor -plain -trustAllHosts 2>&1 | tee /tmp/turbo_output.txt

echo
echo "Expected: 30,1002-071808 (3 files x 10 lines each)"
echo "Actual output:"
cat /tmp/turbo_output.txt

echo
echo "=== Server log excerpts ==="
echo "Turbo aggregate logs:"
grep -E "(TurboAggregate|Processing batch|Serializing)" /tmp/turbo_debug.log | tail -50

# Cleanup
pkill -f "dserver.*port 8888" || true