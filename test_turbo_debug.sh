#!/bin/bash
set -e

echo "Debugging turbo mode output differences..."

# Create simple test file
cat > test_simple.txt << EOF
line 1
line 2
line 3
EOF

# Test without turbo
echo "=== Without Turbo Mode ==="
unset DTAIL_TURBOBOOST_ENABLE
./dserver --cfg none --logger stdout --logLevel error --bindAddress localhost --port 4260 >/dev/null 2>&1 &
SERVER_PID=$!
sleep 1
./dcat --cfg none --servers localhost:4260 --files test_simple.txt --trustAllHosts --plain 2>/dev/null | tee output_noturbo.txt
kill $SERVER_PID 2>/dev/null || true
sleep 1

# Test with turbo
echo -e "\n=== With Turbo Mode ==="
export DTAIL_TURBOBOOST_ENABLE=yes
./dserver --cfg none --logger stdout --logLevel error --bindAddress localhost --port 4261 >/dev/null 2>&1 &
SERVER_PID=$!
sleep 1
./dcat --cfg none --servers localhost:4261 --files test_simple.txt --trustAllHosts --plain 2>/dev/null | tee output_turbo.txt
kill $SERVER_PID 2>/dev/null || true

echo -e "\n=== Comparing outputs ==="
echo "No turbo lines: $(wc -l < output_noturbo.txt)"
echo "Turbo lines: $(wc -l < output_turbo.txt)"
echo -e "\nDiff:"
diff -u output_noturbo.txt output_turbo.txt || true

# Cleanup
rm -f test_simple.txt output_noturbo.txt output_turbo.txt