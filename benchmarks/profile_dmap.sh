#!/bin/bash

# Profile script specifically for dmap with MapReduce format data

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

# Create directories
mkdir -p "$PROFILE_DIR"
mkdir -p "$TEST_DATA_DIR"

echo -e "${GREEN}DTail dmap Profiling${NC}"
echo "===================="
echo

# Function to generate MapReduce format test data
generate_mapreduce_data() {
    local filename=$1
    local lines=$2
    
    if [ ! -f "$filename" ]; then
        echo -e "${YELLOW}Generating MapReduce format test data: $filename${NC}"
        echo "  Command: Creating $filename with $lines lines"
        
        cat > "$filename" << EOF
STATS|earth|2024-01-01T10:00:00.000Z|goroutines:50;openFiles:120;connections:15;currentConnections:5;lifetimeConnections:1500
STATS|mars|2024-01-01T10:00:01.000Z|goroutines:45;openFiles:110;connections:12;currentConnections:4;lifetimeConnections:1200
STATS|venus|2024-01-01T10:00:02.000Z|goroutines:60;openFiles:130;connections:20;currentConnections:8;lifetimeConnections:2000
EOF
        
        # Repeat the pattern to create larger file
        for i in $(seq 1 $lines); do
            hostname="host$((i % 10))"
            # Simple timestamp generation without date command
            hour=$((10 + (i / 3600) % 24))
            min=$(((i / 60) % 60))
            sec=$((i % 60))
            timestamp=$(printf "2024-01-01T%02d:%02d:%02d.000Z" $hour $min $sec)
            goroutines=$((40 + i % 40))
            openFiles=$((100 + i % 50))
            connections=$((10 + i % 20))
            currentConnections=$((i % 10))
            lifetimeConnections=$((1000 + i))
            
            echo "STATS|$hostname|$timestamp|goroutines:$goroutines;openFiles:$openFiles;connections:$connections;currentConnections:$currentConnections;lifetimeConnections:$lifetimeConnections" >> "$filename"
        done
    fi
}

# Generate test data
echo -e "${GREEN}Preparing MapReduce test data...${NC}"
generate_mapreduce_data "$TEST_DATA_DIR/stats_small.log" 1000
generate_mapreduce_data "$TEST_DATA_DIR/stats_medium.log" 10000

# Build dmap
echo -e "${GREEN}Building commands...${NC}"
echo "  Command: cd .. && make dmap"
cd ..
make dmap 2>/dev/null || true
cd "$SCRIPT_DIR"

echo

# Profile different dmap queries
echo -e "${GREEN}Profiling dmap queries...${NC}"

# Query 1: Simple count
echo -e "\n${YELLOW}Query: Count by hostname${NC}"
QUERY="from STATS select count(\$line) group by \$hostname outfile $TEST_DATA_DIR/count_output.csv"
echo "Command: timeout 30s ../dmap -profile -profiledir $PROFILE_DIR -plain -cfg none -query \"$QUERY\" -files $TEST_DATA_DIR/stats_small.log"
timeout 30s ../dmap -profile -profiledir "$PROFILE_DIR" -plain -cfg none -query "$QUERY" -files "$TEST_DATA_DIR/stats_small.log" 2>&1 | head -5

# Query 2: Aggregations
echo -e "\n${YELLOW}Query: Sum and average${NC}"
QUERY="from STATS select sum(\$goroutines),avg(\$goroutines) group by \$hostname outfile $TEST_DATA_DIR/sum_avg_output.csv"
echo "Command: timeout 30s ../dmap -profile -profiledir $PROFILE_DIR -plain -cfg none -query \"$QUERY\" -files $TEST_DATA_DIR/stats_small.log"
timeout 30s ../dmap -profile -profiledir "$PROFILE_DIR" -plain -cfg none -query "$QUERY" -files "$TEST_DATA_DIR/stats_small.log" 2>&1 | head -5

# Query 3: Min/Max
echo -e "\n${YELLOW}Query: Min and max${NC}"
QUERY="from STATS select min(currentConnections),max(lifetimeConnections) group by \$hostname outfile $TEST_DATA_DIR/min_max_output.csv"
echo "Command: timeout 30s ../dmap -profile -profiledir $PROFILE_DIR -plain -cfg none -query \"$QUERY\" -files $TEST_DATA_DIR/stats_small.log"
timeout 30s ../dmap -profile -profiledir "$PROFILE_DIR" -plain -cfg none -query "$QUERY" -files "$TEST_DATA_DIR/stats_small.log" 2>&1 | head -5

echo
echo -e "${GREEN}Analyzing dmap profiles...${NC}"

# Find and analyze latest dmap profiles
DMAP_CPU=$(ls -t "$PROFILE_DIR"/dmap_cpu_*.prof 2>/dev/null | head -1)
if [ -n "$DMAP_CPU" ]; then
    echo -e "\nCPU Profile: $(basename "$DMAP_CPU")"
    ../profiling/profile.sh -top 5 "$DMAP_CPU" 2>/dev/null || echo "  Analysis failed"
fi

DMAP_MEM=$(ls -t "$PROFILE_DIR"/dmap_mem_*.prof 2>/dev/null | head -1)
if [ -n "$DMAP_MEM" ]; then
    echo -e "\nMemory Profile: $(basename "$DMAP_MEM")"
    ../profiling/profile.sh -top 5 "$DMAP_MEM" 2>/dev/null || echo "  Analysis failed"
fi

echo
echo -e "${GREEN}dmap profiling complete!${NC}"
echo
echo "To analyze profiles in detail:"
echo "  go tool pprof $PROFILE_DIR/dmap_cpu_*.prof"
echo "  go tool pprof -alloc_space $PROFILE_DIR/dmap_mem_*.prof"

# Cleanup temporary output files
rm -f "$TEST_DATA_DIR"/*_output.csv