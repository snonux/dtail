#!/bin/bash

# Test script to run dcat integration test with turbo mode enabled

echo "Running dcat integration test with turbo mode and trace logging"

# Enable turbo mode
export DTAIL_TURBOBOOST_ENABLE=yes

# Enable integration test mode
export DTAIL_INTEGRATION_TEST_RUN_MODE=yes

# Clean up old files
cd integrationtests
rm -f *.tmp *.log

echo "Starting dserver with trace logging..."
../dserver --cfg none --logger stdout --logLevel trace --bindAddress localhost --port 9999 > dserver_trace.log 2>&1 &
SERVER_PID=$!

# Give server time to start
sleep 1

echo "Running dcat with turbo mode..."
../dcat --plain --cfg none --servers localhost:9999 --files dcat1a.txt --trustAllHosts --noColor > dcat1_turbo_output.tmp 2> dcat1_turbo_client.log

echo "Killing server..."
kill $SERVER_PID
wait $SERVER_PID 2>/dev/null

echo "Comparing output..."
if diff dcat1_turbo_output.tmp dcat1a.txt; then
    echo "SUCCESS: Output matches expected"
else
    echo "FAILURE: Output differs from expected"
    echo "Expected lines: $(wc -l < dcat1a.txt)"
    echo "Actual lines: $(wc -l < dcat1_turbo_output.tmp)"
    echo "First 10 lines of diff:"
    diff dcat1_turbo_output.tmp dcat1a.txt | head -10
fi

echo "Server log tail:"
tail -20 dserver_trace.log