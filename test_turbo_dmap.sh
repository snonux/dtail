#!/bin/bash

# Test turbo mode dmap output
set -e

echo "Creating test data..."
TEST_DATA="/tmp/dtail_mapreduce_test.log"
> $TEST_DATA
for i in {1..100}; do
    echo "2023-12-27 10:00:00 server1 component=TestApp level=INFO message=Test goroutines=34 connections=10" >> $TEST_DATA
    echo "2023-12-27 10:00:01 server2 component=TestApp level=INFO message=Test goroutines=35 connections=20" >> $TEST_DATA
done

echo "Files created, running queries..."

# Simple query
QUERY='select count($server),$server from - group by $server'

echo "=== Regular mode ==="
unset DTAIL_TURBOBOOST_ENABLE
./dmap -servers localhost:2222 -files "$TEST_DATA" -query "$QUERY" -noColor -plain 2>&1 | head -10

echo
echo "=== Turbo mode ==="
export DTAIL_TURBOBOOST_ENABLE=yes
./dmap -servers localhost:2222 -files "$TEST_DATA" -query "$QUERY" -noColor -plain 2>&1 | head -10