#\!/bin/bash
echo "Starting server with test_server.json config..."
DTAIL_TURBOBOOST_ENABLE=yes ../dserver --cfg test_server.json --logLevel debug --bindAddress localhost --port 4360 2>&1 | grep -E "MaxConcurrent|limiter|config" | head -10 &
SERVER_PID=$\!

sleep 2
echo ""
echo "Killing server..."
kill $SERVER_PID 2>/dev/null || true
