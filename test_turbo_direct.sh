#!/bin/bash
set -e

echo "Testing direct turbo mode..."

# Create test file
echo "test line 1" > test_direct.txt
echo "test line 2" >> test_direct.txt

# Start server
DTAIL_TURBOBOOST_ENABLE=yes ./dserver --cfg none --logger stdout --logLevel info --bindAddress localhost --port 4254 >server.log 2>&1 &
SERVER_PID=$!
sleep 2

# Run dcat capturing both stdout and stderr
DTAIL_TURBOBOOST_ENABLE=yes ./dcat --cfg none --servers localhost:4254 --files test_direct.txt --trustAllHosts --plain >client.out 2>client.err

# Kill server
kill $SERVER_PID 2>/dev/null || true

echo "=== Client Output ==="
cat client.out

echo -e "\n=== Server Turbo Messages ==="
grep -E "(turbo|direct|Turbo)" server.log || echo "No turbo messages found"

echo -e "\n=== Server Errors ==="
grep -E "(ERROR|error)" server.log | tail -5 || echo "No errors"

# Cleanup
rm -f test_direct.txt server.log client.out client.err