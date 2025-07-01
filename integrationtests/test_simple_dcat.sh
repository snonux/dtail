#\!/bin/bash
# Simple test to understand the 4-file issue

echo "Starting server..."
DTAIL_TURBOBOOST_ENABLE=yes ../dserver --cfg none --logLevel trace --bindAddress localhost --port 4350 > server_trace.log 2>&1 &
SERVER_PID=$\!

sleep 1

echo "Running client with 10 files..."
FILES=$(python3 -c "print(','.join(['dcat2.txt']*10))")
DTAIL_TURBOBOOST_ENABLE=yes ../dcat --plain --logLevel trace --cfg none --servers localhost:4350 --trustAllHosts --noColor --files "$FILES" > client_output.txt 2>&1

echo "Client completed"
sleep 1

kill $SERVER_PID 2>/dev/null

echo "Files processed: $(grep -c '^498 Sat  2 Oct 13:46:46 EEST 2021$' client_output.txt)"
echo ""
echo "Server shutdown events:"
grep -E "shutdown|Waiting for pending|pending|Processing files" server_trace.log | tail -20
echo ""
echo "Client final lines:"
tail -20 client_output.txt | grep -v "^[0-9]* Sat"
