#!/bin/bash
set -e

echo "Testing turbo mode activation..."

# Start server
DTAIL_TURBOBOOST_ENABLE=yes ./dserver --cfg none --logger stdout --logLevel info --bindAddress localhost --port 4250 2>server_turbo.log &
SERVER_PID=$!
sleep 2

# Run dcat
DTAIL_TURBOBOOST_ENABLE=yes ./dcat --cfg none --servers localhost:4250 --files README.md --trustAllHosts --plain 2>client_turbo.log >/dev/null

# Kill server
kill $SERVER_PID 2>/dev/null || true

echo "=== Server turbo messages ==="
grep -i "turbo" server_turbo.log || echo "No turbo messages in server log"

echo -e "\n=== Client turbo messages ==="
grep -i "turbo" client_turbo.log || echo "No turbo messages in client log"

# Test serverless mode
echo -e "\n=== Testing serverless mode ==="
DTAIL_TURBOBOOST_ENABLE=yes ./dcat --cfg none --plain README.md 2>serverless_turbo.log >/dev/null
grep -i "turbo" serverless_turbo.log || echo "No turbo messages in serverless mode"

# Cleanup
rm -f server_turbo.log client_turbo.log serverless_turbo.log