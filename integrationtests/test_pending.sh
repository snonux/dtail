#!/bin/bash
# Test pending files tracking with low concurrency limit

echo "Starting server with debug logging..."
../dserver --cfg none --logLevel debug --bindAddress localhost --port 4300 > test_pending_server.log 2>&1 &
SERVER_PID=$!

sleep 1

echo "Running client with 10 files..."
DTAIL_TURBOBOOST_ENABLE=yes ../dcat --plain --logLevel error --cfg none --servers localhost:4300 --trustAllHosts --noColor --files dcat2.txt,dcat2.txt,dcat2.txt,dcat2.txt,dcat2.txt,dcat2.txt,dcat2.txt,dcat2.txt,dcat2.txt,dcat2.txt > test_pending_client.out 2>&1

echo "Client completed. Checking output..."
LINES=$(wc -l < test_pending_client.out)
echo "Got $LINES lines (expected 5000)"

echo "Killing server..."
kill $SERVER_PID 2>/dev/null

echo "Server log excerpts:"
grep -E "Server limit|pending|Processing files|shutdown|Command finished" test_pending_server.log | tail -20

echo "Line count check:"
if [ "$LINES" -eq 5000 ]; then
    echo "SUCCESS: All files processed"
else
    echo "FAILURE: Only $LINES lines out of 5000"
fi