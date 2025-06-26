#!/bin/bash
# Benchmark management script for DTail

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASELINES_DIR="${SCRIPT_DIR}/baselines"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to print usage
usage() {
    cat << EOF
DTail Benchmark Management Tool

Usage: $0 [command] [options]

Commands:
    baseline                Create a new baseline snapshot
    compare [baseline]      Compare current performance against a baseline
    list                    List available baselines
    show [baseline]         Display a baseline file
    clean                   Remove old baselines (keeps last 10)
    full-baseline           Create a comprehensive baseline (all benchmarks, 3x iterations)

Options:
    -o, --output FILE       Save benchmark output to custom file
    -t, --tag TAG          Add a descriptive tag to baseline filename
    -q, --quick            Run quick benchmarks only
    -m, --memory           Include memory profiling
    -c, --cpu-profile      Generate CPU profile
    -v, --verbose          Show detailed output

Examples:
    # Create a baseline before optimization
    $0 baseline --tag "before-optimization"
    
    # Compare current performance with baseline
    $0 compare benchmarks/baselines/baseline_20240125_143022_before-optimization.txt
    
    # Create full baseline with memory stats
    $0 full-baseline --memory --tag "v1.0-release"

EOF
}

# Function to ensure baselines directory exists
ensure_baselines_dir() {
    mkdir -p "$BASELINES_DIR"
}

# Function to create baseline
create_baseline() {
    local tag=""
    local bench_args="-bench=."
    local output_file=""
    local memory_profile=""
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            -t|--tag)
                tag="_$2"
                shift 2
                ;;
            -q|--quick)
                bench_args="-bench=BenchmarkQuick"
                shift
                ;;
            -m|--memory)
                memory_profile="-benchmem"
                shift
                ;;
            -o|--output)
                output_file="$2"
                shift 2
                ;;
            *)
                shift
                ;;
        esac
    done
    
    # If no tag provided, ask for one
    if [ -z "$tag" ]; then
        echo -e "${YELLOW}Creating benchmark baseline...${NC}"
        read -p "Enter a descriptive name for this baseline (e.g. 'before-optimization', 'v1.0-release'): " tag_input
        if [ -z "$tag_input" ]; then
            echo -e "${RED}Error: Baseline name cannot be empty${NC}"
            exit 1
        fi
        # Clean the tag input
        tag="_$(echo "$tag_input" | tr ' ' '_' | tr -cd '[:alnum:]._-')"
    fi
    
    ensure_baselines_dir
    
    if [ -z "$output_file" ]; then
        output_file="${BASELINES_DIR}/baseline_${TIMESTAMP}${tag}.txt"
    fi
    
    echo -e "${GREEN}Creating baseline: ${output_file}${NC}"
    echo "Git commit: $(git rev-parse --short HEAD)" > "$output_file"
    echo "Date: $(date)" >> "$output_file"
    echo "Tag: ${tag#_}" >> "$output_file"
    echo "----------------------------------------" >> "$output_file"
    
    cd "$SCRIPT_DIR/.."
    make build
    go test $bench_args $memory_profile ./benchmarks -count=1 | tee -a "$output_file"
    
    echo -e "${GREEN}Baseline created: ${output_file}${NC}"
}

# Function to create full baseline
create_full_baseline() {
    local tag=""
    local memory_profile=""
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            -t|--tag)
                tag="_$2"
                shift 2
                ;;
            -m|--memory)
                memory_profile="-benchmem"
                shift
                ;;
            *)
                shift
                ;;
        esac
    done
    
    # If no tag provided, ask for one
    if [ -z "$tag" ]; then
        echo -e "${YELLOW}Creating comprehensive benchmark baseline...${NC}"
        read -p "Enter a descriptive name for this baseline (e.g. 'before-optimization', 'v1.0-release'): " tag_input
        if [ -z "$tag_input" ]; then
            echo -e "${RED}Error: Baseline name cannot be empty${NC}"
            exit 1
        fi
        # Clean the tag input
        tag="_$(echo "$tag_input" | tr ' ' '_' | tr -cd '[:alnum:]._-')"
    fi
    
    ensure_baselines_dir
    
    local output_file="${BASELINES_DIR}/baseline_${TIMESTAMP}${tag}_full.txt"
    
    echo -e "${GREEN}Creating comprehensive baseline: ${output_file}${NC}"
    echo "Git commit: $(git rev-parse --short HEAD)" > "$output_file"
    echo "Date: $(date)" >> "$output_file"
    echo "Tag: ${tag#_} (full)" >> "$output_file"
    echo "----------------------------------------" >> "$output_file"
    
    cd "$SCRIPT_DIR/.."
    make build
    
    # Run with multiple iterations for stability
    go test -bench=. $memory_profile -benchtime=3x ./benchmarks -count=1 | tee -a "$output_file"
    
    echo -e "${GREEN}Full baseline created: ${output_file}${NC}"
}

# Function to compare with baseline
compare_baseline() {
    local baseline_file="$1"
    
    if [ -z "$baseline_file" ]; then
        echo -e "${RED}Error: No baseline file specified${NC}"
        echo "Available baselines:"
        list_baselines
        exit 1
    fi
    
    if [ ! -f "$baseline_file" ]; then
        echo -e "${RED}Error: Baseline file not found: $baseline_file${NC}"
        exit 1
    fi
    
    ensure_baselines_dir
    local current_file="${BASELINES_DIR}/current_${TIMESTAMP}.txt"
    
    echo -e "${YELLOW}Running current benchmarks...${NC}"
    echo "Git commit: $(git rev-parse --short HEAD)" > "$current_file"
    echo "Date: $(date)" >> "$current_file"
    echo "----------------------------------------" >> "$current_file"
    
    cd "$SCRIPT_DIR/.."
    make build
    go test -bench=. -benchmem ./benchmarks -count=1 | tee -a "$current_file"
    
    echo -e "\n${YELLOW}=== Performance Comparison ===${NC}"
    
    # Use benchstat if available
    if command -v benchstat >/dev/null 2>&1; then
        benchstat "$baseline_file" "$current_file"
    else
        echo -e "${YELLOW}benchstat not found. Install with:${NC}"
        echo "  go install golang.org/x/perf/cmd/benchstat@latest"
        echo -e "\n${YELLOW}Showing simple comparison:${NC}"
        
        # Extract benchmark results for comparison
        echo -e "\nBaseline ($(basename "$baseline_file")):"
        grep "^Benchmark" "$baseline_file" | head -10
        
        echo -e "\nCurrent:"
        grep "^Benchmark" "$current_file" | head -10
    fi
    
    # Save comparison report
    local report_file="${BASELINES_DIR}/comparison_${TIMESTAMP}.txt"
    {
        echo "Comparison Report"
        echo "================"
        echo "Baseline: $baseline_file"
        echo "Current: $current_file"
        echo "Date: $(date)"
        echo ""
        if command -v benchstat >/dev/null 2>&1; then
            benchstat "$baseline_file" "$current_file"
        else
            diff -u "$baseline_file" "$current_file" || true
        fi
    } > "$report_file"
    
    echo -e "\n${GREEN}Comparison report saved: $report_file${NC}"
}

# Function to list baselines
list_baselines() {
    ensure_baselines_dir
    
    echo -e "${YELLOW}Available baselines:${NC}"
    if [ -d "$BASELINES_DIR" ]; then
        ls -la "$BASELINES_DIR"/*.txt 2>/dev/null | awk '{print $9, $6, $7, $8}' | column -t || echo "No baselines found"
    else
        echo "No baselines found"
    fi
}

# Function to show baseline content
show_baseline() {
    local baseline_file="$1"
    
    if [ -z "$baseline_file" ]; then
        echo -e "${RED}Error: No baseline file specified${NC}"
        list_baselines
        exit 1
    fi
    
    if [ ! -f "$baseline_file" ]; then
        echo -e "${RED}Error: Baseline file not found: $baseline_file${NC}"
        exit 1
    fi
    
    less "$baseline_file"
}

# Function to clean old baselines
clean_baselines() {
    ensure_baselines_dir
    
    echo -e "${YELLOW}Cleaning old baselines (keeping last 10)...${NC}"
    
    # Count files
    local file_count=$(ls -1 "$BASELINES_DIR"/*.txt 2>/dev/null | wc -l)
    
    if [ "$file_count" -gt 10 ]; then
        # Remove oldest files, keeping last 10
        ls -t "$BASELINES_DIR"/*.txt | tail -n +11 | xargs rm -v
        echo -e "${GREEN}Cleanup complete${NC}"
    else
        echo "No cleanup needed (only $file_count baselines found)"
    fi
}

# Main command handling
case "${1:-}" in
    baseline)
        shift
        create_baseline "$@"
        ;;
    full-baseline)
        shift
        create_full_baseline "$@"
        ;;
    compare)
        shift
        compare_baseline "$@"
        ;;
    list)
        list_baselines
        ;;
    show)
        shift
        show_baseline "$@"
        ;;
    clean)
        clean_baselines
        ;;
    -h|--help|help)
        usage
        ;;
    *)
        echo -e "${RED}Error: Unknown command '${1:-}'${NC}"
        usage
        exit 1
        ;;
esac