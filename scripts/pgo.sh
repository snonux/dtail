#!/bin/bash

# Performance Guided Optimization (PGO) script for dgrep
# This script implements true PGO using Go's -pgo compiler flag:
# 1. Build baseline version
# 2. Generate CPU profile for training
# 3. Rebuild with PGO using the profile
# 4. Compare before/after performance

set -e

# Global variables
setup_environment() {
    # Get the directory where this script is located
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    # Get the project root directory (parent of scripts)
    PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
    
    # Change to project root to run commands
    cd "$PROJECT_ROOT"
    
    # Define paths for all PGO files in scripts directory
    PGO_DIR="$SCRIPT_DIR"
    TEST_FILE="$PGO_DIR/test_100mb.txt"
    BASELINE_CPU_PROF="$PGO_DIR/pgo_baseline_cpu.prof"
    BASELINE_MEM_PROF="$PGO_DIR/pgo_baseline_mem.prof"
    TRAINING_PROF="$PGO_DIR/pgo_training.prof"
    OPTIMIZED_CPU_PROF="$PGO_DIR/pgo_optimized_cpu.prof"
    OPTIMIZED_MEM_PROF="$PGO_DIR/pgo_optimized_mem.prof"
    REPORT_FILE="$PGO_DIR/pgo_report.txt"
    
    echo "=== Starting Profile Guided Optimization (PGO) for dgrep ==="
    echo "Working directory: $PROJECT_ROOT"
    echo "PGO files location: $PGO_DIR"
}

create_test_file() {
    echo "1. Creating test file if needed..."
    if [ ! -f "$TEST_FILE" ]; then
        echo "Creating 100MB test file with 1M lines..."
        for i in $(seq 1 1000000); do
            echo "$i: This is a test line with INFO level logging and some extra content to make it realistic"
        done > "$TEST_FILE"
    fi
}

build_baseline() {
    echo "2. Building baseline version (without PGO)..."
    # Clean any existing binaries
    rm -f dgrep dgrep_pgo dcat dmap dtail dserver dtailhealth
    go build -tags '' -o dgrep ./cmd/dgrep/main.go
}

collect_training_data() {
    echo "3. Running baseline performance test and collecting training profile..."
    echo "   - Generating baseline CPU and memory profiles..."
    ./dgrep --plain -regex "INFO" -files "$TEST_FILE" -cpuprofile "$BASELINE_CPU_PROF" -memprofile "$BASELINE_MEM_PROF" > /dev/null
    
    echo "   - Collecting training profile for PGO..."
    ./dgrep --plain -regex "INFO" -files "$TEST_FILE" -cpuprofile "$TRAINING_PROF" > /dev/null
}

build_pgo_optimized() {
    echo "4. Building PGO-optimized version using training profile..."
    go build -tags '' -pgo="$TRAINING_PROF" -o dgrep_pgo ./cmd/dgrep/main.go
}

run_pgo_performance_test() {
    echo "5. Running PGO-optimized performance test..."
    echo "   - Generating optimized CPU and memory profiles..."
    ./dgrep_pgo --plain -regex "INFO" -files "$TEST_FILE" -cpuprofile "$OPTIMIZED_CPU_PROF" -memprofile "$OPTIMIZED_MEM_PROF" > /dev/null
}

run_performance_comparison() {
    echo "6. Running performance comparison..."
    echo "=== PROFILE GUIDED OPTIMIZATION REPORT ===" > "$REPORT_FILE"
    echo "Generated: $(date)" >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    
    echo "BASELINE (without PGO):" >> "$REPORT_FILE"
    echo "Baseline performance (5 iterations):" >> "$REPORT_FILE"
    for i in 1 2 3 4 5; do
        echo "   Iteration $i:"
        { time ./dgrep --plain -regex "INFO" -files "$TEST_FILE" > /dev/null; } 2>&1 | grep real >> "$REPORT_FILE"
    done
    
    echo "" >> "$REPORT_FILE"
    echo "PGO-OPTIMIZED:" >> "$REPORT_FILE"
    echo "PGO-optimized performance (5 iterations):" >> "$REPORT_FILE"
    for i in 1 2 3 4 5; do
        echo "   Iteration $i:"
        { time ./dgrep_pgo --plain -regex "INFO" -files "$TEST_FILE" > /dev/null; } 2>&1 | grep real >> "$REPORT_FILE"
    done
}

generate_detailed_analysis() {
    echo "7. Adding detailed profile analysis..."
    echo "" >> "$REPORT_FILE"
    echo "DETAILED ANALYSIS:" >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    echo "Baseline CPU Profile:" >> "$REPORT_FILE"
    go tool pprof -top "$BASELINE_CPU_PROF" | head -10 >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    echo "PGO-Optimized CPU Profile:" >> "$REPORT_FILE"
    go tool pprof -top "$OPTIMIZED_CPU_PROF" | head -10 >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    echo "Baseline Memory Profile:" >> "$REPORT_FILE"
    go tool pprof -top "$BASELINE_MEM_PROF" | head -10 >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    echo "PGO-Optimized Memory Profile:" >> "$REPORT_FILE"
    go tool pprof -top "$OPTIMIZED_MEM_PROF" | head -10 >> "$REPORT_FILE"
}

cleanup() {
    echo "8. Cleaning up..."
    rm -f dgrep_pgo
}

show_summary() {
    echo "=== PGO Complete! ==="
    echo "Results saved to: $REPORT_FILE"
    echo "Profile files generated:"
    echo "  - Baseline: $BASELINE_CPU_PROF, $BASELINE_MEM_PROF"
    echo "  - Training: $TRAINING_PROF"
    echo "  - Optimized: $OPTIMIZED_CPU_PROF, $OPTIMIZED_MEM_PROF"
    echo ""
    echo "Test file location: $TEST_FILE"
    echo ""
    echo "PGO Process:"
    echo "  ✓ Built baseline version without PGO"
    echo "  ✓ Collected CPU profile for training"
    echo "  ✓ Rebuilt with Go's -pgo flag using training profile"
    echo "  ✓ Compared baseline vs PGO-optimized performance"
    echo ""
    
    # Show performance comparison from report
    echo "=== Performance Comparison ==="
    echo "Check $REPORT_FILE for detailed before/after comparison"
    grep -A 20 "BASELINE (without PGO)" "$REPORT_FILE" | head -10
    echo "..."
    grep -A 20 "PGO-OPTIMIZED" "$REPORT_FILE" | head -10
}

# Main execution flow
main() {
    setup_environment
    create_test_file
    build_baseline
    collect_training_data
    build_pgo_optimized
    run_pgo_performance_test
    run_performance_comparison
    generate_detailed_analysis
    cleanup
    show_summary
}

# Run the main function
main "$@"