#!/bin/bash

for COUNT in 5 10 20 50 100; do
    echo "=== Testing with $COUNT files ==="
    
    # Build file list
    FILES=""
    for i in $(seq 1 $COUNT); do
        if [ -n "$FILES" ]; then
            FILES="${FILES},mapr_testdata.log"
        else
            FILES="mapr_testdata.log"
        fi
    done
    
    # Start server
    DTAIL_TURBOBOOST_ENABLE=yes ../dserver --cfg none --logger stdout --logLevel error --bindAddress localhost --port 4260 >/dev/null 2>&1 &
    SERVER_PID=$!
    sleep 2
    
    # Run test
    DTAIL_TURBOBOOST_ENABLE=yes timeout 30 ../dmap --cfg none --noColor \
        --query "from STATS select count(\$time),\$time group by \$time limit 1" \
        --servers localhost:4260 --trustAllHosts \
        --files "$FILES" 2>&1 | grep -E "(Writing to|exit status)"
    
    kill $SERVER_PID 2>/dev/null
    sleep 1
done