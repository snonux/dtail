#!/bin/bash
set -e

echo "Debugging turbo mode..."

# Create a small test file
echo "test line 1" > test_turbo.txt
echo "test line 2" >> test_turbo.txt
echo "test line 3" >> test_turbo.txt

# Start server with debug logging
DTAIL_TURBOBOOST_ENABLE=yes ./dserver --cfg none --logger stdout --logLevel debug --bindAddress localhost --port 4251 2>&1 | tee server_debug.log &
SERVER_PID=$!
sleep 2

# Run dcat with debug
DTAIL_TURBOBOOST_ENABLE=yes ./dcat --cfg none --servers localhost:4251 --files test_turbo.txt --trustAllHosts --plain 2>&1 | tee client_debug.log

# Kill server
kill $SERVER_PID 2>/dev/null || true
sleep 1

echo -e "\n=== Turbo-related messages ==="
grep -E "(turbo|Turbo|channel-less|direct writer)" server_debug.log | tail -20

# Cleanup
rm -f test_turbo.txt server_debug.log client_debug.log