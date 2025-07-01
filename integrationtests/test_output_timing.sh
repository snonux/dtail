#\!/bin/bash
# Test to see how long it takes for client to complete

echo "Starting server..."
DTAIL_TURBOBOOST_ENABLE=yes ../dserver --cfg none --logLevel info --bindAddress localhost --port 4356 > server_info.log 2>&1 &
SERVER_PID=$\!

sleep 1

echo "Running client..."
FILES=$(python3 -c "print(','.join(['dcat2.txt']*100))")
time DTAIL_TURBOBOOST_ENABLE=yes ../dcat --plain --logLevel error --cfg none --servers localhost:4356 --trustAllHosts --noColor --files "$FILES" > client_output_timing.txt 2>&1

echo "Client completed"
kill $SERVER_PID 2>/dev/null || true

echo "Files processed: $(grep -c '^498 Sat  2 Oct 13:46:46 EEST 2021$' client_output_timing.txt)"
echo "Server log lines: $(wc -l < server_info.log)"
echo ""
echo "Last server log entries:"
tail -10 server_info.log
