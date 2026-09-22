package integrationtests

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/config"
)

// TestDServerScheduledRetryAfterFailure runs a scheduled job twice, as two
// dserver runs within the same date period. In the first run the job's server
// is not reachable (connection refused): the job fails and must leave no
// outfile, .query or temporary file behind, so it is not skipped for the rest
// of the period. In the second run dserver listens on the job's server port:
// the job runs again and writes the same outfile a job that succeeds at once
// writes (dserver1.csv.expected, same query and data).
func TestDServerScheduledRetryAfterFailure(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}
	cleanupTmpFiles(t)
	testLogger := NewTestLogger("TestDServerScheduledRetryAfterFailure")
	defer writeLogFileIgnoringError(testLogger)

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := WithTestLogger(baseCtx, testLogger)

	const (
		cfgFile   = "dserver4.cfg.tmp"
		csvFile   = "dserver4.csv.tmp"
		queryFile = csvFile + ".query"
		query     = "from STATS select count($line),last($time),avg($goroutines)," +
			"min(concurrentConnections),max(lifetimeConnections) group by $hostname"
	)
	jobPort := getUniquePortNumber()
	otherPort := getUniquePortNumber()
	cfg := fmt.Sprintf(`{"Server": {"Schedule": [{
  "Name": "dserver_schedule_retry_test", "Enable": true,
  "AllowFrom": ["localhost"], "TimeRange": [0, 24],
  "Servers": ["127.0.0.1:%d"], "Files": "./mapr_testdata.log",
  "Query": %q, "Outfile": "./%s"}]}}`, jobPort, query, csvFile)
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// cleanupTmpFiles leaves the .query file of an earlier run of this test.
	if err := os.Remove(queryFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	// First run: nothing listens on the job's server port.
	output := runScheduledDServer(ctx, t, cfgFile, otherPort, 5)
	if !strings.Contains(output, "Job dserver_schedule_retry_test failed and wrote no outfile") {
		t.Errorf("first run did not log the failed job, output:\n%s", output)
	}
	for _, path := range []string{csvFile, queryFile, csvFile + ".tmp", queryFile + ".tmp"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed job left %s (stat error %v)", path, err)
		}
	}

	// Second run: dserver itself is the job's server.
	output = runScheduledDServer(ctx, t, cfgFile, jobPort, 10)
	if strings.Contains(output, "Not running job as outfile already exists") {
		t.Errorf("second run skipped the job, output:\n%s", output)
	}
	if err := compareFilesWithContext(ctx, t, csvFile, "dserver1.csv.expected"); err != nil {
		t.Error(err)
	}
	if err := compareFilesWithContext(ctx, t, queryFile, "dserver4.csv.query.expected"); err != nil {
		t.Error(err)
	}
}

// runScheduledDServer runs dserver with cfgFile on port until it shuts down
// after shutdownAfter seconds and returns its standard output.
func runScheduledDServer(ctx context.Context, t *testing.T, cfgFile string, port, shutdownAfter int) string {
	t.Helper()
	stdoutCh, stderrCh, cmdErrCh, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", cfgFile,
		"--logger", "stdout",
		"--logLevel", "info",
		"--bindAddress", "127.0.0.1",
		"--shutdownAfter", fmt.Sprintf("%d", shutdownAfter),
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	for {
		select {
		case line, ok := <-stdoutCh:
			if ok {
				t.Log(line)
				output.WriteString(line)
				output.WriteString("\n")
			}
		case line, ok := <-stderrCh:
			if ok {
				t.Log(line)
			}
		case cmdErr := <-cmdErrCh:
			t.Logf("dserver finished with exit code %d: %v", exitCodeFromError(cmdErr), cmdErr)
			return output.String()
		}
	}
}
