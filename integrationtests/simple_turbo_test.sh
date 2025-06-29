#!/bin/bash

# Test with just 3 files to see if it works at all
echo "=== Testing with 3 files (same name) ==="

# Start server
DTAIL_TURBOBOOST_ENABLE=yes ../dserver --cfg none --logger stdout --logLevel info --bindAddress localhost --port 4250 &
SERVER_PID=$!
sleep 3

# Run with 3 same files
../dmap --cfg none --noColor \
    --query "from STATS select count(\$time),\$time group by \$time order by count(\$time) desc outfile test_3same.csv" \
    --servers localhost:4250 --trustAllHosts \
    --files mapr_testdata.log,mapr_testdata.log,mapr_testdata.log

if [ -f test_3same.csv ]; then
    echo "Success! Output file created"
    echo "Lines: $(wc -l < test_3same.csv)"
    echo "Sample:"
    head -5 test_3same.csv
else
    echo "FAILED: No output file"
fi

kill $SERVER_PID 2>/dev/null
sleep 1

echo -e "\n=== Testing with 3 files (different names) ==="
cp mapr_testdata.log test1.log
cp mapr_testdata.log test2.log  
cp mapr_testdata.log test3.log

# Start server
DTAIL_TURBOBOOST_ENABLE=yes ../dserver --cfg none --logger stdout --logLevel info --bindAddress localhost --port 4251 &
SERVER_PID=$!
sleep 3

# Run with 3 different files
../dmap --cfg none --noColor \
    --query "from STATS select count(\$time),\$time group by \$time order by count(\$time) desc outfile test_3diff.csv" \
    --servers localhost:4251 --trustAllHosts \
    --files test1.log,test2.log,test3.log

if [ -f test_3diff.csv ]; then
    echo "Success! Output file created"
    echo "Lines: $(wc -l < test_3diff.csv)"
    echo "Sample:"
    head -5 test_3diff.csv
else
    echo "FAILED: No output file"
fi

kill $SERVER_PID 2>/dev/null

# Cleanup
rm -f test*.log test_*.csv