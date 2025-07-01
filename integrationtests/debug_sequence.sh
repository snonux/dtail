#!/bin/bash
# Debug script to understand the sequence of events with limited concurrency

echo "Starting server with trace logging..."
../dserver --cfg none --logLevel trace --bindAddress localhost --port 4344 2>&1 | tee debug_server_trace.log &
SERVER_PID=$!

sleep 1

echo "Running client with 100 files (50x the concurrency limit)..."
FILES=$(python3 -c "print(','.join(['dcat2.txt']*100))")
DTAIL_TURBOBOOST_ENABLE=yes ../dcat --plain --logLevel debug --cfg none --servers localhost:4344 --trustAllHosts --noColor --files "$FILES" > debug_client_output.txt 2>&1 &
CLIENT_PID=$!

# Wait for client to finish or timeout
sleep 15
if ps -p $CLIENT_PID > /dev/null; then
    echo "Client still running after 15 seconds, killing..."
    kill $CLIENT_PID
fi

# Kill server
kill $SERVER_PID 2>/dev/null

echo "Client output lines: $(wc -l < debug_client_output.txt)"
echo ""
echo "Key server events:"
grep -E "Server limit hit|Got limiter slot|pending|Processing files|shutdown|close connection|Command finished|Waiting for pending" debug_server_trace.log | tail -30
echo ""
echo "Client errors:"
grep -E "error|Error|EOF|close" debug_client_output.txt | tail -10