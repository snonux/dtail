#!/bin/bash
set -e

echo "Testing Turbo Mode in Serverless Mode"
echo "====================================="

# Create test data
TEST_FILE="/tmp/turbo_test_data.log"
echo "Generating test data..."
for i in {1..100000}; do
    echo "2025-06-30T12:00:00.${i}Z INFO user$((i % 100)) performed action$((i % 10)) in $((i % 1000))ms"
done > "$TEST_FILE"

echo "Test file size: $(du -h $TEST_FILE | cut -f1)"

# Function to run benchmark
run_test() {
    local mode=$1
    local iterations=5
    
    if [ "$mode" == "turbo" ]; then
        export DTAIL_TURBOBOOST_ENABLE=yes
    else
        unset DTAIL_TURBOBOOST_ENABLE
    fi
    
    echo -e "\nRunning $mode mode (serverless)..."
    
    # Run multiple iterations and calculate average
    total_time=0
    for i in $(seq 1 $iterations); do
        start_time=$(date +%s.%N)
        ./dcat --cfg none "$TEST_FILE" > /dev/null 2>&1
        end_time=$(date +%s.%N)
        elapsed=$(echo "$end_time - $start_time" | bc)
        total_time=$(echo "$total_time + $elapsed" | bc)
        echo "  Iteration $i: ${elapsed}s"
    done
    
    avg_time=$(echo "scale=3; $total_time / $iterations" | bc)
    echo "  Average: ${avg_time}s"
    
    return 0
}

# Build with modified turbo condition
echo -e "\nModifying turbo mode to work in serverless mode..."
cp internal/server/handlers/readcommand.go internal/server/handlers/readcommand.go.bak

# Remove the serverless restriction
sed -i 's/turboBoostEnabled && !r.server.serverless/turboBoostEnabled/' internal/server/handlers/readcommand.go

# Build the modified version
echo "Building modified version..."
make build

# Run tests
echo -e "\nRunning benchmarks..."
run_test "normal"
NORMAL_TIME=$avg_time

run_test "turbo"
TURBO_TIME=$avg_time

# Calculate improvement
improvement=$(echo "scale=2; (($NORMAL_TIME - $TURBO_TIME) / $NORMAL_TIME) * 100" | bc)
echo -e "\nPerformance improvement: ${improvement}%"
echo "Normal mode: ${NORMAL_TIME}s"
echo "Turbo mode: ${TURBO_TIME}s"

# Restore original file
mv internal/server/handlers/readcommand.go.bak internal/server/handlers/readcommand.go

# Cleanup
rm -f "$TEST_FILE"

echo -e "\nDone!"