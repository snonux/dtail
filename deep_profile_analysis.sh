#!/bin/bash
set -e

echo "Deep Profile Analysis for Turbo Mode"
echo "===================================="

# Create test data
TEST_FILE="/tmp/profile_test_data.log"
echo "Generating larger test data..."
for i in {1..1000000}; do
    echo "2025-06-30T12:00:00.${i}Z INFO user$((i % 100)) performed action$((i % 10)) in $((i % 1000))ms"
done > "$TEST_FILE"

echo "Test file size: $(du -h $TEST_FILE | cut -f1)"

# Function to run profiled test
run_profiled_test() {
    local mode=$1
    local profile_dir="/tmp/dtail_profiles_$mode"
    mkdir -p "$profile_dir"
    
    if [ "$mode" == "turbo" ]; then
        export DTAIL_TURBOBOOST_ENABLE=yes
    else
        unset DTAIL_TURBOBOOST_ENABLE
    fi
    
    echo -e "\nRunning $mode mode with profiling..."
    
    # Run with CPU profiling
    ./dcat --cfg none --profile --profiledir "$profile_dir" "$TEST_FILE" > /dev/null 2>&1
    
    # Find the CPU profile
    cpu_profile=$(ls -t "$profile_dir"/dcat_cpu_*.prof | head -1)
    
    echo "CPU Profile: $cpu_profile"
    
    # Extract top functions
    echo -e "\nTop 10 CPU consumers:"
    go tool pprof -top -nodecount=10 "$cpu_profile" | grep -A 12 "flat  flat%"
    
    # Check for specific functions
    echo -e "\nChecking for key functions:"
    go tool pprof -text "$cpu_profile" | grep -E "(channel|Channel|selectgo|processor|Processor|turbo|optimized)" | head -20 || echo "No matching functions found"
    
    # Look at cumulative time
    echo -e "\nTop cumulative time consumers:"
    go tool pprof -cum -top -nodecount=5 "$cpu_profile" | grep -A 7 "flat  flat%"
}

# Run both modes
run_profiled_test "normal"
run_profiled_test "turbo"

# Also check what's happening with strace
echo -e "\nChecking system calls with strace..."
strace -c ./dcat --cfg none "$TEST_FILE" > /dev/null 2>&1

# Cleanup
rm -f "$TEST_FILE"

echo -e "\nDone!"