#!/bin/bash

# Comprehensive benchmark: Channel-based vs Channelless Cat Implementation
# Tests performance improvements achieved by eliminating channel overhead

set -e

echo "=== DTail Channelless Performance Benchmark ==="
echo "Comparing channel-based vs channelless cat implementation"
echo "Date: $(date)"
echo

# Test configuration
TEST_FILES=("test_100mb.txt" "test_200mb.txt")
ITERATIONS=5
WARMUP_RUNS=2

# Results storage
RESULTS_DIR="benchmark_results_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$RESULTS_DIR"

# Ensure we're in the correct directory
cd "$(dirname "$0")/.."

# Build both implementations
echo "Building DTail binaries..."
make clean > /dev/null 2>&1
make build > /dev/null 2>&1
echo "✓ Build complete"
echo

# Function to run benchmark for a specific configuration
run_benchmark() {
    local use_channelless=$1
    local test_file=$2
    local impl_name=$3
    local results_file="$RESULTS_DIR/${impl_name}_$(basename $test_file .txt).results"
    
    echo "Testing $impl_name with $test_file..."
    
    # Warmup runs
    for ((i=1; i<=WARMUP_RUNS; i++)); do
        echo -n "  Warmup $i/$WARMUP_RUNS... "
        DTAIL_USE_CHANNELLESS=$use_channelless DTAIL_INTEGRATION_TEST_RUN_MODE=yes \
            timeout 30s ./dcat --logLevel error --cfg none "scripts/$test_file" > /dev/null 2>&1
        echo "done"
    done
    
    # Actual benchmark runs
    echo "  Running $ITERATIONS benchmark iterations:"
    for ((i=1; i<=ITERATIONS; i++)); do
        echo -n "    Run $i/$ITERATIONS... "
        
        # Clear caches
        sync
        echo 3 > /proc/sys/vm/drop_caches 2>/dev/null || true
        
        # Run benchmark with time measurement
        start_time=$(date +%s.%N)
        DTAIL_USE_CHANNELLESS=$use_channelless DTAIL_INTEGRATION_TEST_RUN_MODE=yes \
            timeout 30s ./dcat --logLevel error --cfg none "scripts/$test_file" > /dev/null 2>&1
        end_time=$(date +%s.%N)
        
        # Calculate duration
        duration=$(echo "$end_time - $start_time" | bc -l)
        echo "$duration" >> "$results_file"
        
        printf "%.3fs\n" "$duration"
    done
    echo
}

# Function to calculate statistics
calculate_stats() {
    local file=$1
    local values=($(cat "$file"))
    local sum=0
    local count=${#values[@]}
    
    # Calculate mean
    for val in "${values[@]}"; do
        sum=$(echo "$sum + $val" | bc -l)
    done
    local mean=$(echo "scale=6; $sum / $count" | bc -l)
    
    # Calculate standard deviation
    local variance_sum=0
    for val in "${values[@]}"; do
        local diff=$(echo "$val - $mean" | bc -l)
        local squared=$(echo "$diff * $diff" | bc -l)
        variance_sum=$(echo "$variance_sum + $squared" | bc -l)
    done
    local variance=$(echo "scale=6; $variance_sum / $count" | bc -l)
    local stddev=$(echo "scale=6; sqrt($variance)" | bc -l)
    
    # Find min and max
    local min=${values[0]}
    local max=${values[0]}
    for val in "${values[@]}"; do
        if (( $(echo "$val < $min" | bc -l) )); then
            min=$val
        fi
        if (( $(echo "$val > $max" | bc -l) )); then
            max=$val
        fi
    done
    
    echo "$mean $stddev $min $max"
}

# Function to calculate throughput
calculate_throughput() {
    local file_size_mb=$1
    local time_seconds=$2
    echo "scale=2; $file_size_mb / $time_seconds" | bc -l
}

# Run benchmarks
echo "Starting benchmarks..."
echo

for test_file in "${TEST_FILES[@]}"; do
    echo "=== Benchmarking with $test_file ==="
    
    # Get file size in MB
    file_size_bytes=$(stat -c%s "scripts/$test_file")
    file_size_mb=$(echo "scale=2; $file_size_bytes / 1024 / 1024" | bc -l)
    echo "File size: ${file_size_mb} MB"
    echo
    
    # Test channel-based implementation
    run_benchmark "false" "$test_file" "channel_based"
    
    # Test channelless implementation  
    run_benchmark "true" "$test_file" "channelless"
    
    echo "--- Results for $test_file ---"
    
    # Calculate statistics for channel-based
    channel_stats=($(calculate_stats "$RESULTS_DIR/channel_based_$(basename $test_file .txt).results"))
    channel_mean=${channel_stats[0]}
    channel_stddev=${channel_stats[1]}
    channel_min=${channel_stats[2]}
    channel_max=${channel_stats[3]}
    channel_throughput=$(calculate_throughput "$file_size_mb" "$channel_mean")
    
    # Calculate statistics for channelless
    channelless_stats=($(calculate_stats "$RESULTS_DIR/channelless_$(basename $test_file .txt).results"))
    channelless_mean=${channelless_stats[0]}
    channelless_stddev=${channelless_stats[1]}
    channelless_min=${channelless_stats[2]}
    channelless_max=${channelless_stats[3]}
    channelless_throughput=$(calculate_throughput "$file_size_mb" "$channelless_mean")
    
    # Calculate improvement
    improvement=$(echo "scale=2; (($channel_mean - $channelless_mean) / $channel_mean) * 100" | bc -l)
    speedup=$(echo "scale=2; $channel_mean / $channelless_mean" | bc -l)
    throughput_improvement=$(echo "scale=2; (($channelless_throughput - $channel_throughput) / $channel_throughput) * 100" | bc -l)
    
    echo "Channel-based:"
    printf "  Time: %.3f ± %.3f seconds (min: %.3f, max: %.3f)\n" "$channel_mean" "$channel_stddev" "$channel_min" "$channel_max"
    printf "  Throughput: %.2f MB/s\n" "$channel_throughput"
    echo
    echo "Channelless:"
    printf "  Time: %.3f ± %.3f seconds (min: %.3f, max: %.3f)\n" "$channelless_mean" "$channelless_stddev" "$channelless_min" "$channelless_max"
    printf "  Throughput: %.2f MB/s\n" "$channelless_throughput"
    echo
    echo "Performance Improvement:"
    printf "  Time reduction: %.2f%% (%.2fx speedup)\n" "$improvement" "$speedup"
    printf "  Throughput increase: %.2f%%\n" "$throughput_improvement"
    echo
    echo "=========================================="
    echo
done

# Generate summary report
echo "=== BENCHMARK SUMMARY ==="
echo

summary_file="$RESULTS_DIR/benchmark_summary.txt"
{
    echo "DTail Channelless Performance Benchmark Summary"
    echo "Date: $(date)"
    echo "Iterations per test: $ITERATIONS"
    echo "Warmup runs: $WARMUP_RUNS"
    echo
    
    for test_file in "${TEST_FILES[@]}"; do
        file_size_bytes=$(stat -c%s "scripts/$test_file")
        file_size_mb=$(echo "scale=2; $file_size_bytes / 1024 / 1024" | bc -l)
        
        channel_stats=($(calculate_stats "$RESULTS_DIR/channel_based_$(basename $test_file .txt).results"))
        channelless_stats=($(calculate_stats "$RESULTS_DIR/channelless_$(basename $test_file .txt).results"))
        
        channel_mean=${channel_stats[0]}
        channelless_mean=${channelless_stats[0]}
        
        improvement=$(echo "scale=2; (($channel_mean - $channelless_mean) / $channel_mean) * 100" | bc -l)
        speedup=$(echo "scale=2; $channel_mean / $channelless_mean" | bc -l)
        
        channel_throughput=$(calculate_throughput "$file_size_mb" "$channel_mean")
        channelless_throughput=$(calculate_throughput "$file_size_mb" "$channelless_mean")
        
        echo "$test_file (${file_size_mb} MB):"
        printf "  Channel-based: %.3f seconds (%.2f MB/s)\n" "$channel_mean" "$channel_throughput"
        printf "  Channelless:   %.3f seconds (%.2f MB/s)\n" "$channelless_mean" "$channelless_throughput"
        printf "  Improvement:   %.2f%% faster (%.2fx speedup)\n" "$improvement" "$speedup"
        echo
    done
} | tee "$summary_file"

echo "Detailed results saved in: $RESULTS_DIR/"
echo "Summary report: $summary_file"
echo
echo "=== BENCHMARK COMPLETE ==="