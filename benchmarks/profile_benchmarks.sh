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
PROFILE_RUNS=3

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

# Generate test data
echo -e "${GREEN}Preparing test data...${NC}"
generate_test_data "10MB" "$TEST_DATA_DIR/small.log"
generate_test_data "100MB" "$TEST_DATA_DIR/medium.log"
generate_test_data "1GB" "$TEST_DATA_DIR/large.log"

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
run_profile "../dcat" "medium_file" "-plain -cfg none $TEST_DATA_DIR/medium.log"
# Skip large file for faster profiling - uncomment if needed
# run_profile "../dcat" "large_file" "-plain -cfg none $TEST_DATA_DIR/large.log"

# Profile dgrep
echo -e "${GREEN}=== Profiling dgrep ===${NC}"
run_profile "../dgrep" "simple_regex" "-plain -cfg none -regex 'user[0-9]+' $TEST_DATA_DIR/medium.log"
run_profile "../dgrep" "complex_regex" "-plain -cfg none -regex '\\d{4}-\\d{2}-\\d{2}.*login.*\\d{3}' $TEST_DATA_DIR/medium.log"
run_profile "../dgrep" "with_context" "-plain -cfg none -regex 'login' -before 2 -after 2 $TEST_DATA_DIR/medium.log"

# Profile dmap
echo -e "${GREEN}=== Profiling dmap ===${NC}"
# Note: dmap uses a special query format for MapReduce operations
# For CSV files, we need to specify the format and fields correctly
echo -e "${YELLOW}Note: Skipping dmap profiling - requires specific log format${NC}"
echo -e "${YELLOW}To profile dmap, use files in MapReduce format with queries like:${NC}"
echo -e "${YELLOW}  from STATS select count(\$line) group by \$hostname${NC}"

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