#!/bin/bash
set -e

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${GREEN}DTail Turbo Mode Benchmark Test${NC}"
echo "================================="

# Create test directory
TEST_DIR="/tmp/dtail_turbo_test"
mkdir -p "$TEST_DIR"

# Generate test data
echo -e "\n${YELLOW}Generating test data...${NC}"
cat > "$TEST_DIR/generate_test_data.py" << 'EOF'
#!/usr/bin/env python3
import random
import sys

size_mb = int(sys.argv[1]) if len(sys.argv) > 1 else 100
lines = size_mb * 10000  # Approximately 100 bytes per line

users = [f"user{i}" for i in range(100)]
actions = ["login", "logout", "view", "edit", "delete", "create", "search", "filter"]

print(f"Generating {size_mb}MB test file with {lines} lines...")

with open("/tmp/dtail_turbo_test/test_data.log", "w") as f:
    for i in range(lines):
        user = random.choice(users)
        action = random.choice(actions)
        duration = random.randint(10, 5000)
        f.write(f"2025-06-30T12:00:00.{i:06d}Z INFO {user} performed {action} in {duration}ms\n")
        
print("Test data generated!")
EOF

python3 "$TEST_DIR/generate_test_data.py" 50

# Create server config
cat > "$TEST_DIR/dtail_server.json" << 'EOF'
{
    "SSHPort": 2223,
    "MaxConcurrentCats": 200,
    "MaxLineLength": 100000,
    "LogDir": "/tmp/dtail_turbo_test/logs",
    "LogLevel": "info"
}
EOF

# Create client config
cat > "$TEST_DIR/dtail_client.json" << 'EOF'
{
    "SSHPort": 2223,
    "UserName": "$USER",
    "Servers": ["localhost"],
    "LogLevel": "info"
}
EOF

# Function to run benchmark
run_benchmark() {
    local mode=$1
    local result_file=$2
    
    echo -e "\n${YELLOW}Running $mode benchmark...${NC}"
    
    # Start server
    if [ "$mode" == "turbo" ]; then
        export DTAIL_TURBOBOOST_ENABLE=yes
    else
        unset DTAIL_TURBOBOOST_ENABLE
    fi
    
    # Start dserver in background
    ./dserver --cfg "$TEST_DIR/dtail_server.json" &
    SERVER_PID=$!
    sleep 2  # Wait for server to start
    
    # Run benchmarks
    echo "Testing dcat..."
    { time ./dcat --cfg "$TEST_DIR/dtail_client.json" --plain "$TEST_DIR/test_data.log" > /dev/null 2>&1; } 2>&1 | grep real | awk '{print "dcat: " $2}' >> "$result_file"
    
    echo "Testing dgrep..."
    { time ./dgrep --cfg "$TEST_DIR/dtail_client.json" --plain --regex "user[0-9]+" "$TEST_DIR/test_data.log" > /dev/null 2>&1; } 2>&1 | grep real | awk '{print "dgrep: " $2}' >> "$result_file"
    
    echo "Testing dgrep with high match rate..."
    { time ./dgrep --cfg "$TEST_DIR/dtail_client.json" --plain --regex "performed" "$TEST_DIR/test_data.log" > /dev/null 2>&1; } 2>&1 | grep real | awk '{print "dgrep_high: " $2}' >> "$result_file"
    
    # Kill server
    kill $SERVER_PID 2>/dev/null || true
    wait $SERVER_PID 2>/dev/null || true
}

# Build binaries
echo -e "\n${YELLOW}Building binaries...${NC}"
make build

# Run benchmarks
echo -e "\n${GREEN}Running benchmarks in server mode...${NC}"

# Run without turbo
echo "NO_TURBO" > "$TEST_DIR/results_noturbo.txt"
run_benchmark "noturbo" "$TEST_DIR/results_noturbo.txt"

# Run with turbo
echo "TURBO" > "$TEST_DIR/results_turbo.txt"
run_benchmark "turbo" "$TEST_DIR/results_turbo.txt"

# Compare results
echo -e "\n${GREEN}Results Comparison:${NC}"
echo "==================="
echo -e "\n${YELLOW}Without Turbo Mode:${NC}"
cat "$TEST_DIR/results_noturbo.txt"
echo -e "\n${YELLOW}With Turbo Mode:${NC}"
cat "$TEST_DIR/results_turbo.txt"

# Calculate improvements
echo -e "\n${GREEN}Performance Improvements:${NC}"
echo "========================="

# Helper function to convert time to milliseconds
time_to_ms() {
    local time_str=$1
    # Remove 'm' and 's' suffixes and convert to milliseconds
    echo "$time_str" | sed 's/m/*60000+/g; s/s/*1000/g' | bc
}

# Compare each command
for cmd in dcat dgrep dgrep_high; do
    noturbo_time=$(grep "^$cmd:" "$TEST_DIR/results_noturbo.txt" | awk '{print $2}')
    turbo_time=$(grep "^$cmd:" "$TEST_DIR/results_turbo.txt" | awk '{print $2}')
    
    if [ -n "$noturbo_time" ] && [ -n "$turbo_time" ]; then
        noturbo_ms=$(time_to_ms "$noturbo_time")
        turbo_ms=$(time_to_ms "$turbo_time")
        improvement=$(echo "scale=2; (($noturbo_ms - $turbo_ms) / $noturbo_ms) * 100" | bc)
        echo "$cmd: ${improvement}% improvement (${noturbo_time} -> ${turbo_time})"
    fi
done

# Cleanup
echo -e "\n${YELLOW}Cleaning up...${NC}"
rm -rf "$TEST_DIR"