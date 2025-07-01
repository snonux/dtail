#!/bin/bash
set -e

# Create test directory
TEST_DIR="/tmp/dtail_turbo_verify"
mkdir -p "$TEST_DIR"

# Generate small test file
echo "Creating test file..."
for i in {1..1000}; do
    echo "2025-06-30T12:00:00.${i}Z INFO user$i performed action in ${i}ms"
done > "$TEST_DIR/test.log"

# Create server config
cat > "$TEST_DIR/server.json" << 'EOF'
{
    "SSHPort": 2224,
    "MaxConcurrentCats": 10,
    "LogDir": "/tmp/dtail_turbo_verify/logs"
}
EOF

# Create client config
cat > "$TEST_DIR/client.json" << 'EOF'
{
    "SSHPort": 2224,
    "UserName": "$USER",
    "Servers": ["localhost"]
}
EOF

echo "Testing turbo mode activation..."

# Test with turbo mode
export DTAIL_TURBOBOOST_ENABLE=yes

# Start server and capture stderr
./dserver --cfg "$TEST_DIR/server.json" 2>"$TEST_DIR/server_turbo.log" &
SERVER_PID=$!
sleep 2

# Run dcat and capture stderr
echo "Running dcat with turbo mode..."
./dcat --cfg "$TEST_DIR/client.json" "$TEST_DIR/test.log" 2>"$TEST_DIR/client_turbo.log" >/dev/null

# Kill server
kill $SERVER_PID 2>/dev/null || true
wait $SERVER_PID 2>/dev/null || true

# Check for turbo mode messages
echo -e "\nChecking for turbo mode activation messages..."
echo "Server stderr:"
grep -i turbo "$TEST_DIR/server_turbo.log" || echo "No turbo messages found in server log"

echo -e "\nClient stderr:"
grep -i turbo "$TEST_DIR/client_turbo.log" || echo "No turbo messages found in client log"

# Also check if we're actually in server mode
echo -e "\nChecking server mode status..."
grep -E "(serverless|server mode)" "$TEST_DIR/server_turbo.log" || true

# Let's also trace what's happening in the code
echo -e "\nRunning with debug output..."
export DTAIL_DEBUG=1
./dserver --cfg "$TEST_DIR/server.json" 2>&1 | head -20 &
SERVER_PID=$!
sleep 2
./dcat --cfg "$TEST_DIR/client.json" "$TEST_DIR/test.log" 2>&1 | grep -E "(turbo|serverless|channel)" || true
kill $SERVER_PID 2>/dev/null || true

# Cleanup
rm -rf "$TEST_DIR"