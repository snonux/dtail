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

# Function to generate MapReduce format test data (generickv format)
generate_mapreduce_data() {
    local filename=$1
    local lines=$2
    
    if [ ! -f "$filename" ]; then
        echo -e "${YELLOW}Generating MapReduce format test data: $filename${NC}"
        echo "  Command: Creating $filename with $lines lines (generickv format)"
        
        # Generate data in generickv format: field1=value1|field2=value2|...
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
            
            echo "table=STATS|hostname=$hostname|timestamp=$timestamp|goroutines=$goroutines|openFiles=$openFiles|connections=$connections|currentConnections=$currentConnections|lifetimeConnections=$lifetimeConnections" >> "$filename"
        done
    fi
}

# Generate test data in DTail default format instead
echo -e "${GREEN}Preparing MapReduce test data...${NC}"

# Function to generate DTail default format test data
generate_dtail_format_data() {
    local filename=$1
    local lines=$2
    
    if [ ! -f "$filename" ]; then
        echo -e "${YELLOW}Generating DTail default format test data: $filename${NC}"
        echo "  Command: Creating $filename with $lines lines (DTail default format)"
        
        # Generate DTail default format log lines
        for i in $(seq 1 $lines); do
            hostname="host$((i % 10))"
            goroutines=$((40 + i % 40))
            cgocalls=$((i % 100))
            cpus=$((1 + i % 8))
            loadavg=$(printf "%.2f" $(echo "scale=2; $i % 100 / 100" | bc))
            uptime="${i}h0m0s"
            connections=$((i % 10))
            lifetime=$((1000 + i))
            
            # DTail default format: INFO|date-time|pid|caller|cpus|goroutines|cgocalls|loadavg|uptime|MAPREDUCE:STATS|key=value|...
            echo "INFO|$(date +%m%d-%H%M%S)|1|stats.go:56|$cpus|$goroutines|$cgocalls|$loadavg|$uptime|MAPREDUCE:STATS|hostname=$hostname|currentConnections=$connections|lifetimeConnections=$lifetime" >> "$filename"
        done
    fi
}

generate_dtail_format_data "$TEST_DATA_DIR/stats_small.log" 100
generate_dtail_format_data "$TEST_DATA_DIR/stats_medium.log" 1000

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
QUERY="from STATS select count(\$line) group by hostname"
echo "Command: ../dmap -profile -profiledir $PROFILE_DIR -plain -cfg none -query \"$QUERY\" -files $TEST_DATA_DIR/stats_small.log"
# Run dmap and let it complete naturally
../dmap -profile -profiledir "$PROFILE_DIR" -plain -cfg none -query "$QUERY" -files "$TEST_DATA_DIR/stats_small.log" 2>&1 | head -10

# Query 2: Aggregations
echo -e "\n${YELLOW}Query: Sum and average${NC}"
QUERY="from STATS select sum(\$goroutines),avg(\$goroutines) group by hostname"
echo "Command: ../dmap -profile -profiledir $PROFILE_DIR -plain -cfg none -query \"$QUERY\" -files $TEST_DATA_DIR/stats_small.log"
../dmap -profile -profiledir "$PROFILE_DIR" -plain -cfg none -query "$QUERY" -files "$TEST_DATA_DIR/stats_small.log" 2>&1 | head -10

# Query 3: Min/Max
echo -e "\n${YELLOW}Query: Min and max${NC}"
QUERY="from STATS select min(currentConnections),max(lifetimeConnections) group by hostname"
echo "Command: ../dmap -profile -profiledir $PROFILE_DIR -plain -cfg none -query \"$QUERY\" -files $TEST_DATA_DIR/stats_small.log"
../dmap -profile -profiledir "$PROFILE_DIR" -plain -cfg none -query "$QUERY" -files "$TEST_DATA_DIR/stats_small.log" 2>&1 | head -10

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

# No cleanup needed - no output files are created during profiling
