# The unit and integration tests; render.sh runs this tape in the
# repository root. The integration tests need the binaries of make build.
# render-cwd: repo
Output testing.gif
Source "_settings.tape"

Type "go test -count=1 ./internal/mapr/..."
Enter
Wait@120s /\$ *$/
Sleep 1s
Type "DTAIL_INTEGRATION_TEST_RUN_MODE=yes go test -count=1 -v -run 'TestDCat1$|TestDMap1$' ./integrationtests | grep -E '^(=== RUN|--- |ok|PASS|FAIL)'"
Enter
Wait@300s /\$ *$/
Sleep 3s
