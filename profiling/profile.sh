#!/bin/bash

# dprofile - Simple profile analysis script for dtail
# A lightweight wrapper around go tool pprof

set -e

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Default values
TOP_N=10
SORT_BY="flat"
LIST_MODE=false
PROFILE_PATH=""

# Usage function
usage() {
    echo "dprofile - Analyze pprof profiles"
    echo ""
    echo "Usage:"
    echo "  dprofile <profile>                    # Analyze a profile"
    echo "  dprofile -list [directory]            # List profiles in directory"
    echo "  dprofile -top N <profile>             # Show top N functions (default: 10)"
    echo "  dprofile -cum <profile>               # Sort by cumulative value"
    echo "  dprofile -web <profile>               # Open web browser with flame graph"
    echo "  dprofile -text <profile>              # Full text report"
    echo "  dprofile -help                        # Show this help"
    echo ""
    echo "Examples:"
    echo "  dprofile profiles/dcat_cpu_*.prof"
    echo "  dprofile -top 20 -cum profiles/dgrep_mem_*.prof"
    echo "  dprofile -list profiles/"
    echo "  dprofile -web profiles/dmap_cpu_*.prof"
    exit 1
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -help|--help|-h)
            usage
            ;;
        -list|--list)
            LIST_MODE=true
            shift
            if [[ $# -gt 0 && ! "$1" =~ ^- ]]; then
                PROFILE_DIR="$1"
                shift
            else
                PROFILE_DIR="."
            fi
            ;;
        -top|--top)
            shift
            TOP_N="$1"
            shift
            ;;
        -cum|--cum)
            SORT_BY="cum"
            shift
            ;;
        -web|--web)
            shift
            if [[ $# -eq 0 ]]; then
                echo "Error: -web requires a profile file"
                exit 1
            fi
            echo -e "${GREEN}Opening web browser for $1...${NC}"
            echo "Press Ctrl+C to stop the server"
            exec go tool pprof -http=:8080 "$1"
            ;;
        -text|--text)
            shift
            if [[ $# -eq 0 ]]; then
                echo "Error: -text requires a profile file"
                exit 1
            fi
            exec go tool pprof -text "$1"
            ;;
        -*)
            echo "Unknown option: $1"
            usage
            ;;
        *)
            PROFILE_PATH="$1"
            shift
            ;;
    esac
done

# List mode
if $LIST_MODE; then
    echo -e "${GREEN}Profile files in $PROFILE_DIR:${NC}"
    echo ""
    
    # Group by tool and type
    declare -A profiles
    
    for file in "$PROFILE_DIR"/*.prof; do
        if [[ -f "$file" ]]; then
            basename=$(basename "$file")
            # Extract tool and type (e.g., dcat_cpu -> "dcat cpu")
            if [[ $basename =~ ^([^_]+)_([^_]+)_.*\.prof$ ]]; then
                tool="${BASH_REMATCH[1]}"
                type="${BASH_REMATCH[2]}"
                key="$tool:$type"
                
                if [[ -z "${profiles[$key]}" ]]; then
                    profiles[$key]="$file"
                else
                    profiles[$key]="${profiles[$key]}|$file"
                fi
            fi
        fi
    done
    
    # Display grouped profiles
    current_tool=""
    for key in $(echo "${!profiles[@]}" | tr ' ' '\n' | sort); do
        IFS=':' read -r tool type <<< "$key"
        
        if [[ "$tool" != "$current_tool" ]]; then
            [[ -n "$current_tool" ]] && echo
            echo -e "${YELLOW}$tool profiles:${NC}"
            current_tool="$tool"
        fi
        
        echo "  $type:"
        IFS='|' read -ra files <<< "${profiles[$key]}"
        for file in "${files[@]}"; do
            size=$(ls -lh "$file" 2>/dev/null | awk '{print $5}')
            timestamp=$(basename "$file" | grep -oE '[0-9]{8}_[0-9]{6}' || echo "unknown")
            echo "    $(basename "$file") ($size) - $timestamp"
        done
    done
    
    [[ -z "$current_tool" ]] && echo "  No profile files found in $PROFILE_DIR"
    exit 0
fi

# Check if profile path provided
if [[ -z "$PROFILE_PATH" ]]; then
    usage
fi

# Check if file exists
if [[ ! -f "$PROFILE_PATH" ]]; then
    echo -e "${RED}Error: Profile file not found: $PROFILE_PATH${NC}"
    exit 1
fi

# Detect profile type
PROFILE_TYPE="unknown"
if go tool pprof -raw "$PROFILE_PATH" 2>/dev/null | grep -q "samples/count"; then
    PROFILE_TYPE="cpu"
elif go tool pprof -raw "$PROFILE_PATH" 2>/dev/null | grep -q "alloc_space"; then
    PROFILE_TYPE="memory"
elif go tool pprof -raw "$PROFILE_PATH" 2>/dev/null | grep -q "inuse_space"; then
    PROFILE_TYPE="memory"
fi

# Analyze profile
echo -e "${GREEN}Profile Analysis: $PROFILE_PATH${NC}"
echo "Type: $PROFILE_TYPE"
echo ""

# Get top functions
echo "Top $TOP_N functions (sorted by $SORT_BY):"
echo "================================================================"

# Use different flags based on sort order
if [[ "$SORT_BY" == "cum" ]]; then
    echo "# Command: go tool pprof -top -cum -nodecount=$TOP_N $PROFILE_PATH"
    go tool pprof -top -cum -nodecount="$TOP_N" "$PROFILE_PATH" 2>/dev/null | \
        grep -E "^[[:space:]]*[0-9]+" | head -n "$TOP_N" || true
else
    echo "# Command: go tool pprof -top -nodecount=$TOP_N $PROFILE_PATH"
    go tool pprof -top -nodecount="$TOP_N" "$PROFILE_PATH" 2>/dev/null | \
        grep -E "^[[:space:]]*[0-9]+" | head -n "$TOP_N" || true
fi

echo ""

# Provide helpful tips based on profile type
if [[ "$PROFILE_TYPE" == "cpu" ]]; then
    echo -e "${YELLOW}CPU Profile Tips:${NC}"
    echo "- flat: time spent in the function itself"
    echo "- cum: time spent in the function and its callees"
    echo "- Focus on functions with high flat% for optimization"
    echo ""
    echo "Interactive exploration:"
    echo "  go tool pprof $PROFILE_PATH"
    echo ""
    echo "Generate flame graph:"
    echo "  dprofile -web $PROFILE_PATH"
elif [[ "$PROFILE_TYPE" == "memory" ]]; then
    echo -e "${YELLOW}Memory Profile Tips:${NC}"
    echo "- Shows memory allocations by function"
    echo "- Focus on unexpected allocations in hot paths"
    echo ""
    echo "View all allocations:"
    echo "  go tool pprof -alloc_space $PROFILE_PATH"
    echo ""
    echo "View in-use memory:"
    echo "  go tool pprof -inuse_space $PROFILE_PATH"
fi