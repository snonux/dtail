#!/bin/bash

# Verification script for dmap turbo mode output
set -e

echo "=== DTail dmap Output Verification ==="
echo "Comparing regular mode vs turbo mode output"
echo

# Create test data if it doesn't exist
TEST_DATA="/tmp/dtail_test_data.log"
if [ ! -f "$TEST_DATA" ]; then
    echo "Creating test data..."
    for i in {1..1000}; do
        echo "2023-12-27 10:00:00 integrationtest mapreduce=TestData goroutines=34.5 lifetimeConnections=0" >> $TEST_DATA
    done
fi

# Test query - simple aggregation without where clause
QUERY='select count($hostname),$hostname,avg($goroutines),sum($lifetimeConnections) from - group by $hostname order by count($hostname)'

# Run in regular mode
echo "Running in regular mode..."
OUTPUT_REGULAR=$(./dmap -servers localhost:2222 -files "$TEST_DATA" -query "$QUERY" 2>/dev/null | head -20)

# Run in turbo mode
echo "Running in turbo mode..."
export DTAIL_TURBOBOOST_ENABLE=yes
OUTPUT_TURBO=$(./dmap -servers localhost:2222 -files "$TEST_DATA" -query "$QUERY" 2>/dev/null | head -20)
unset DTAIL_TURBOBOOST_ENABLE

# Compare outputs
echo
echo "=== Regular Mode Output ==="
echo "$OUTPUT_REGULAR" | head -5
echo "Lines: $(echo "$OUTPUT_REGULAR" | wc -l)"
echo

echo "=== Turbo Mode Output ==="
echo "$OUTPUT_TURBO" | head -5
echo "Lines: $(echo "$OUTPUT_TURBO" | wc -l)"
echo

# Check if outputs match
if [ "$OUTPUT_REGULAR" = "$OUTPUT_TURBO" ]; then
    echo "✓ PASS: Outputs match exactly!"
else
    echo "✗ FAIL: Outputs differ!"
    echo
    echo "Difference:"
    diff <(echo "$OUTPUT_REGULAR") <(echo "$OUTPUT_TURBO") || true
fi