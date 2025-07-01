#!/bin/bash
set -e

echo "Tracing turbo mode execution..."

# Create test file
echo "test line 1" > test_trace.txt

# Start server with stderr captured
DTAIL_TURBOBOOST_ENABLE=yes ./dserver --cfg none --logger stdout --logLevel info --bindAddress localhost --port 4255 2>server_stderr.log &
SERVER_PID=$!
sleep 2

# Run dcat
DTAIL_TURBOBOOST_ENABLE=yes ./dcat --cfg none --servers localhost:4255 --files test_trace.txt --trustAllHosts --plain >/dev/null 2>&1

# Kill server
kill $SERVER_PID 2>/dev/null || true
sleep 1

echo "=== Server stderr (turbo messages) ==="
cat server_stderr.log | grep -E "(DTAIL|turbo|direct)" || echo "No turbo messages"

# Cleanup
rm -f test_trace.txt server_stderr.log