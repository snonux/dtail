#\!/bin/bash
# Simple test to see if CombinedOutput captures turbo mode output

echo "Starting server..."
DTAIL_TURBOBOOST_ENABLE=yes ../dserver --cfg test_server.json --logLevel error --bindAddress localhost --port 4357 &
SERVER_PID=$\!

sleep 1

echo "Running client with CombinedOutput simulation..."
FILES=$(python3 -c "print(','.join(['dcat2.txt']*5))")
DTAIL_TURBOBOOST_ENABLE=yes ../dcat --plain --logLevel error --cfg none --servers localhost:4357 --trustAllHosts --noColor --files "$FILES" 2>&1 | tee combined_output.txt

kill $SERVER_PID 2>/dev/null || true

echo "Lines captured: $(wc -l < combined_output.txt)"
echo "Files processed: $(grep -c '^498 Sat  2 Oct 13:46:46 EEST 2021$' combined_output.txt)"
