#!/bin/bash

# Quick profile script for dtail commands
# This runs profiling with smaller datasets for faster results

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Default values
PROFILE_DIR="${PROFILE_DIR:-profiles}"
TEST_DATA_DIR="${TEST_DATA_DIR:-testdata}"

# Create directories
mkdir -p "$PROFILE_DIR"
mkdir -p "$TEST_DATA_DIR"

echo -e "${GREEN}DTail Quick Profiling${NC}"
echo "====================="
echo

# Generate test data if needed
if [ ! -f "$TEST_DATA_DIR/quick_test.log" ]; then
    echo -e "${YELLOW}Generating test data...${NC}"
    echo "  Command: go run ../benchmarks/cmd/generate_profile_data.go -size \"10MB\" -output \"$TEST_DATA_DIR/quick_test.log\" -format log"
    go run ../benchmarks/cmd/generate_profile_data.go -size "10MB" -output "$TEST_DATA_DIR/quick_test.log" -format log
    echo "  Command: go run ../benchmarks/cmd/generate_profile_data.go -size \"10MB\" -output \"$TEST_DATA_DIR/quick_test.csv\" -format csv"
    go run ../benchmarks/cmd/generate_profile_data.go -size "10MB" -output "$TEST_DATA_DIR/quick_test.csv" -format csv
fi

# Build commands
echo -e "${GREEN}Building commands...${NC}"
echo "  Command: cd .. && make dcat dgrep dmap"
cd ..
make dcat dgrep dmap 2>/dev/null || true
cd "$SCRIPT_DIR"

echo
echo -e "${GREEN}Running quick profiles...${NC}"

# Profile dcat
echo -e "\n${YELLOW}Profiling dcat...${NC}"
echo "Command: ../dcat -profile -profiledir $PROFILE_DIR -plain -cfg none $TEST_DATA_DIR/quick_test.log"
../dcat -profile -profiledir "$PROFILE_DIR" -plain -cfg none "$TEST_DATA_DIR/quick_test.log" > /dev/null 2>&1
DCAT_CPU=$(ls -t "$PROFILE_DIR"/dcat_cpu_*.prof 2>/dev/null | head -1)
if [ -n "$DCAT_CPU" ]; then
    echo "  Generated: $(basename "$DCAT_CPU")"
    echo "  Analysis: ../profiling/profile.sh -top 3 $DCAT_CPU"
    ../profiling/profile.sh -top 3 "$DCAT_CPU" | grep -A 5 "Top 3 functions"
fi

# Profile dgrep
echo -e "\n${YELLOW}Profiling dgrep...${NC}"
echo "Command: ../dgrep -profile -profiledir $PROFILE_DIR -plain -cfg none -regex \"user[0-9]+\" $TEST_DATA_DIR/quick_test.log"
../dgrep -profile -profiledir "$PROFILE_DIR" -plain -cfg none -regex "user[0-9]+" "$TEST_DATA_DIR/quick_test.log" > /dev/null 2>&1
DGREP_CPU=$(ls -t "$PROFILE_DIR"/dgrep_cpu_*.prof 2>/dev/null | head -1)
if [ -n "$DGREP_CPU" ]; then
    echo "  Generated: $(basename "$DGREP_CPU")"
    echo "  Analysis: ../profiling/profile.sh -top 3 $DGREP_CPU"
    ../profiling/profile.sh -top 3 "$DGREP_CPU" | grep -A 5 "Top 3 functions"
fi

# Profile dmap (use proper MapReduce query on CSV file)
echo -e "\n${YELLOW}Profiling dmap...${NC}"
QUERY="select count($line),avg($duration) group by $user logformat csv"
echo "Command: ../dmap -profile -profiledir $PROFILE_DIR -plain -cfg none -query \"$QUERY\" -files $TEST_DATA_DIR/quick_test.csv (will interrupt after 3s)"
# Run dmap in background and interrupt after 3 seconds
../dmap -profile -profiledir "$PROFILE_DIR" -plain -cfg none -query "$QUERY" -files "$TEST_DATA_DIR/quick_test.csv" > /dev/null 2>&1 &
DMAP_PID=$!
sleep 3
kill -INT $DMAP_PID 2>/dev/null || true
wait $DMAP_PID 2>/dev/null || true

DMAP_CPU=$(ls -t "$PROFILE_DIR"/dmap_cpu_*.prof 2>/dev/null | head -1)
if [ -n "$DMAP_CPU" ]; then
    echo "  Generated: $(basename "$DMAP_CPU")"
    echo "  Analysis: ../profiling/profile.sh -top 3 $DMAP_CPU"
    ../profiling/profile.sh -top 3 "$DMAP_CPU" | grep -A 5 "Top 3 functions"
fi

echo
echo -e "${GREEN}Quick profiling complete!${NC}"
echo
echo "To analyze in detail:"
echo "  go tool pprof $PROFILE_DIR/<profile_file>"
echo "  make profile-flamegraph PROFILE=$PROFILE_DIR/<profile_file>"
echo