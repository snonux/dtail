#!/bin/bash

# Test with small number of files to debug
set -e

echo "=== Testing Turbo Mode with 3 Files ==="

# Kill any existing servers
pkill -f "dserver.*port 9999" || true
sleep 1

# Start server with turbo mode and debug logging
echo "Starting turbo server..."
DTAIL_TURBOBOOST_ENABLE=yes ./dserver --cfg integrationtests/test_server_100files.json --logLevel debug --bindAddress localhost --port 9999 > /tmp/turbo_test.log 2>&1 &
SERVER_PID=$!
sleep 2

# Run query with just 3 files
QUERY='from STATS select count($time),$time from - group by $time order by count($time) desc'

echo "Running dmap query with 3 files..."
./dmap -servers localhost:9999 -files "integrationtests/mapr_testdata.log,integrationtests/mapr_testdata.log,integrationtests/mapr_testdata.log" -query "$QUERY" -noColor -plain -trustAllHosts > /tmp/output.csv 2>&1

echo "Output:"
cat /tmp/output.csv | head -10

echo
echo "Checking for specific timestamp (should be 69 = 23 * 3):"
grep "1002-071147" /tmp/output.csv || echo "Not found in output"

echo
echo "=== Server logs ==="
echo "Files processed:"
grep -c "Flush completed" /tmp/turbo_test.log || echo "0"

echo
echo "Turbo aggregate summary:"
grep -E "(Shutdown called|files.*processed|Serialization details)" /tmp/turbo_test.log | tail -20

# Cleanup
kill $SERVER_PID 2>/dev/null || true