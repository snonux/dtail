#!/bin/bash

# Profile benchmarks script for dtail commands
# This script runs profiling on dcat, dgrep, and dmap with various workloads

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Default values
PROFILE_DIR="${PROFILE_DIR:-profiles}"
TEST_DATA_DIR="${TEST_DATA_DIR:-testdata}"
PROFILE_RUNS=1

# Create directories
mkdir -p "$PROFILE_DIR"
mkdir -p "$TEST_DATA_DIR"

echo -e "${GREEN}DTail Profiling Framework${NC}"
echo "=========================="
echo

# Function to generate test data
generate_test_data() {
    local size=$1
    local filename=$2
    
    if [ ! -f "$filename" ]; then
        echo -e "${YELLOW}Generating test data: $filename (${size})${NC}"
        # Use the standalone generator
        echo "  Command: go run generate_profile_data.go -size \"${size}\" -output \"$filename\" -format log"
        go run generate_profile_data.go -size "${size}" -output "$filename" -format log
    fi
}

# Function to run profiling
run_profile() {
    local cmd=$1
    local name=$2
    local args=$3
    
    echo -e "${GREEN}Profiling $cmd - $name${NC}"
    
    for i in $(seq 1 $PROFILE_RUNS); do
        echo "  Run $i/$PROFILE_RUNS..."
        echo "  Command: timeout 30s $cmd -profile -profiledir $PROFILE_DIR $args"
        
        # Run with CPU and memory profiling with timeout
        timeout 30s $cmd -profile -profiledir "$PROFILE_DIR" $args > /dev/null 2>&1
        local exit_code=$?
        
        if [ $exit_code -eq 124 ]; then
            echo -e "  ${YELLOW}Warning: Run $i timed out after 30s${NC}"
        elif [ $exit_code -ne 0 ]; then
            echo -e "  ${RED}Error: Run $i failed with exit code $exit_code${NC}"
        fi
        
        # Small delay between runs
        sleep 1
    done
    
    echo
}

# Special function for profiling dmap which runs continuously
run_profile_dmap() {
    local cmd=$1
    local name=$2
    local args=$3
    
    echo -e "${GREEN}Profiling $cmd - $name${NC}"
    
    for i in $(seq 1 $PROFILE_RUNS); do
        echo "  Run $i/$PROFILE_RUNS..."
        echo "  Command: $cmd -profile -profiledir $PROFILE_DIR $args (will interrupt after 3s)"
        
        # Run dmap in background, wait a bit for it to process, then interrupt it
        $cmd -profile -profiledir "$PROFILE_DIR" $args > /dev/null 2>&1 &
        local pid=$!
        
        # Wait for dmap to process the file and generate initial results
        sleep 3
        
        # Send interrupt signal to make it exit cleanly
        # We expect this to return non-zero, so we ignore the exit code
        kill -INT $pid 2>/dev/null || true
        wait $pid 2>/dev/null || true
        
        echo "  Completed"
        
        # Small delay between runs
        sleep 1
    done
    
    echo
}

# Generate test data
echo -e "${GREEN}Preparing test data...${NC}"
generate_test_data "1MB" "$TEST_DATA_DIR/small.log"
generate_test_data "10MB" "$TEST_DATA_DIR/medium.log"
# Skip large file for faster testing
# generate_test_data "1GB" "$TEST_DATA_DIR/large.log"

# Generate CSV data for dmap (smaller size for faster processing)
if [ ! -f "$TEST_DATA_DIR/test.csv" ]; then
    echo -e "${YELLOW}Generating CSV test data${NC}"
    echo "  Command: go run generate_profile_data.go -size \"10MB\" -output \"$TEST_DATA_DIR/test.csv\" -format csv"
    go run generate_profile_data.go -size "10MB" -output "$TEST_DATA_DIR/test.csv" -format csv
fi

echo

# Build commands
echo -e "${GREEN}Building commands...${NC}"
echo "  Command: cd .. && make dcat dgrep dmap"
cd ..
make dcat dgrep dmap
cd "$SCRIPT_DIR"

echo

# Profile dcat
echo -e "${GREEN}=== Profiling dcat ===${NC}"
run_profile "../dcat" "small_file" "-plain -cfg none $TEST_DATA_DIR/small.log"
# Skip medium file for faster profiling
# run_profile "../dcat" "medium_file" "-plain -cfg none $TEST_DATA_DIR/medium.log"
# Skip large file for faster profiling - uncomment if needed
# run_profile "../dcat" "large_file" "-plain -cfg none $TEST_DATA_DIR/large.log"

# Profile dgrep
echo -e "${GREEN}=== Profiling dgrep ===${NC}"
run_profile "../dgrep" "simple_regex" "-plain -cfg none -regex 'user[0-9]+' $TEST_DATA_DIR/small.log"
# Use small file for faster profiling
# run_profile "../dgrep" "complex_regex" "-plain -cfg none -regex '\\d{4}-\\d{2}-\\d{2}.*login.*\\d{3}' $TEST_DATA_DIR/medium.log"
# run_profile "../dgrep" "with_context" "-plain -cfg none -regex 'login' -before 2 -after 2 $TEST_DATA_DIR/medium.log"

# Profile dmap
echo -e "${GREEN}=== Profiling dmap ===${NC}"

# Generate DTail default format test data for dmap
if [ ! -f "$TEST_DATA_DIR/dtail_format.log" ]; then
    echo -e "${YELLOW}Generating DTail format test data for dmap${NC}"
    echo "  Command: Creating DTail format log file"
    # Generate DTail default format log lines
    for i in $(seq 1 1000); do
        hostname="host$((i % 10))"
        goroutines=$((40 + i % 40))
        cgocalls=$((i % 100))
        cpus=$((1 + i % 8))
        loadavg=$(printf "%.2f" $(echo "scale=2; $i % 100 / 100" | bc))
        uptime="${i}h0m0s"
        connections=$((i % 10))
        lifetime=$((1000 + i))
        
        echo "INFO|$(date +%m%d-%H%M%S)|1|stats.go:56|$cpus|$goroutines|$cgocalls|$loadavg|$uptime|MAPREDUCE:STATS|currentConnections=$connections|lifetimeConnections=$lifetime"
    done > "$TEST_DATA_DIR/dtail_format.log"
fi

# Profile dmap with DTail format  
run_profile_dmap "../dmap" "simple_count" "-plain -cfg none -query 'from STATS select count(*)' -files $TEST_DATA_DIR/dtail_format.log"
run_profile_dmap "../dmap" "aggregations" "-plain -cfg none -query 'from STATS select sum(\$goroutines),avg(\$cgocalls),max(lifetimeConnections)' -files $TEST_DATA_DIR/dtail_format.log"
run_profile_dmap "../dmap" "group_by_connections" "-plain -cfg none -query 'from STATS select currentConnections,count(*) group by currentConnections' -files $TEST_DATA_DIR/dtail_format.log"

# Also test CSV format
echo -e "\n${YELLOW}Testing CSV format with dmap${NC}"
run_profile_dmap "../dmap" "csv_query" "-plain -cfg none -query 'select user,action,count(*) where status=\"success\" group by user,action logformat csv' -files $TEST_DATA_DIR/test.csv"

echo
echo -e "${GREEN}Profiling complete!${NC}"
echo

# Analyze profiles
echo -e "${GREEN}=== Profile Analysis ===${NC}"
echo "Profile files generated in: $PROFILE_DIR"
echo

# List recent profiles
echo "Recent CPU profiles:"
ls -lt "$PROFILE_DIR"/*_cpu_*.prof 2>/dev/null | head -5 || echo "  No CPU profiles found"

echo
echo "Recent memory profiles:"
ls -lt "$PROFILE_DIR"/*_mem_*.prof 2>/dev/null | head -5 || echo "  No memory profiles found"

echo
echo "Recent allocation profiles:"
ls -lt "$PROFILE_DIR"/*_alloc_*.prof 2>/dev/null | head -5 || echo "  No allocation profiles found"

echo
echo -e "${GREEN}To analyze a profile, use:${NC}"
echo "  go tool pprof <profile_file>"
echo "  ../profiling/profile.sh <profile_file>"
echo
echo -e "${GREEN}Examples:${NC}"
echo "  # Interactive analysis"
echo "  go tool pprof $PROFILE_DIR/dcat_cpu_*.prof"
echo
echo "  # Generate flame graph"
echo "  go tool pprof -http=:8080 $PROFILE_DIR/dcat_cpu_*.prof"
echo
echo "  # Quick summary with dprofile"
echo "  ../profiling/profile.sh $PROFILE_DIR/dcat_cpu_*.prof"
echo