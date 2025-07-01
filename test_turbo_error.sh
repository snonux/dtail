#!/bin/bash
set -e

echo "Testing turbo mode for errors..."

# Create test file
echo "test line 1" > test_error.txt
echo "test line 2" >> test_error.txt

# Start server capturing all output
DTAIL_TURBOBOOST_ENABLE=yes ./dserver --cfg none --logger stdout --logLevel debug --bindAddress localhost --port 4256 >server_full.log 2>&1 &
SERVER_PID=$!
sleep 2

# Run dcat capturing all output
DTAIL_TURBOBOOST_ENABLE=yes ./dcat --cfg none --servers localhost:4256 --files test_error.txt --trustAllHosts --plain >client_out.txt 2>client_err.txt

# Kill server
kill $SERVER_PID 2>/dev/null || true
sleep 1

echo "=== Client Output ==="
cat client_out.txt

echo -e "\n=== Client Errors ==="
cat client_err.txt

echo -e "\n=== Server Errors/Warnings ==="
grep -E "(ERROR|WARN|panic|error)" server_full.log | tail -10 || echo "No errors found"

echo -e "\n=== Turbo Messages ==="
grep -E "(turbo|direct|Turbo)" server_full.log | tail -10

# Cleanup
rm -f test_error.txt server_full.log client_out.txt client_err.txt