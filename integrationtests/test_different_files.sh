#!/bin/bash
cd /home/paul/git/dtail/integrationtests

echo "=== Creating 100 copies of the test file with different names ==="
for i in {1..100}; do
    cp mapr_testdata.log "mapr_testdata_${i}.log"
done

echo "=== Running test with different file names ==="
FILES=""
for i in {1..100}; do
    if [ -n "$FILES" ]; then
        FILES="${FILES},mapr_testdata_${i}.log"
    else
        FILES="mapr_testdata_${i}.log"
    fi
done

# Start server
DTAIL_TURBOBOOST_ENABLE=yes ../dserver --cfg none --logger stdout --logLevel error --bindAddress localhost --port 4247 >/dev/null 2>&1 &
SERVER_PID=$!
sleep 2

# Run dmap
DTAIL_TURBOBOOST_ENABLE=yes ../dmap --cfg none --noColor \
    --query "from STATS select count(\$time),\$time,max(\$goroutines),avg(\$goroutines),min(\$goroutines) group by \$time order by count(\$time) desc outfile test_different.csv" \
    --servers localhost:4247 --trustAllHosts --files "$FILES"

echo "Exit code: $?"

# Check results
if [ -f test_different.csv ]; then
    TOTAL=$(awk -F, 'NR>1 {sum+=$1} END {print sum}' test_different.csv)
    echo "Total lines processed: $TOTAL"
    echo "Expected: 59700"
    echo "Missing: $((59700 - TOTAL))"
else
    echo "No output file created"
fi

kill $SERVER_PID 2>/dev/null

# Compare with same file names
echo -e "\n=== Running test with same file names ==="
FILES=""
for i in {1..100}; do
    if [ -n "$FILES" ]; then
        FILES="${FILES},mapr_testdata.log"
    else
        FILES="mapr_testdata.log"
    fi
done

# Start server again
DTAIL_TURBOBOOST_ENABLE=yes ../dserver --cfg none --logger stdout --logLevel error --bindAddress localhost --port 4248 >/dev/null 2>&1 &
SERVER_PID=$!
sleep 2

# Run dmap
DTAIL_TURBOBOOST_ENABLE=yes ../dmap --cfg none --noColor \
    --query "from STATS select count(\$time),\$time,max(\$goroutines),avg(\$goroutines),min(\$goroutines) group by \$time order by count(\$time) desc outfile test_same.csv" \
    --servers localhost:4248 --trustAllHosts --files "$FILES"

echo "Exit code: $?"

# Check results
if [ -f test_same.csv ]; then
    TOTAL=$(awk -F, 'NR>1 {sum+=$1} END {print sum}' test_same.csv)
    echo "Total lines processed: $TOTAL"
    echo "Expected: 59700"
    echo "Missing: $((59700 - TOTAL))"
else
    echo "No output file created"
fi

kill $SERVER_PID 2>/dev/null

# Cleanup
rm -f mapr_testdata_*.log test_different.csv test_same.csv