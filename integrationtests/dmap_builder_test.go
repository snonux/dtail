package integrationtests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// builderLogFile is the log the query builder tests read: builderStatsLines
// STATS lines of the default log format, written once.
const (
	builderLogFile    = "dmap_builder.log.tmp"
	builderStatsLines = 30
)

// Texts of the query builder (internal/clients/interactive_builder.go).
const (
	builderBannerPrefix = "No query given: build one"
	builderRunning      = "running the query"
	builderRejected     = "query builder: invalid query"
	// builderResultHeader starts the result table of builderQueryRest: its
	// header and dashes, which neither the typed line nor the echo holds.
	builderResultHeader = " count($line) \r\n---"
	// builderRejectedQuery is a query that does not parse and, unlike the
	// query built, holds no "from STATS ".
	builderRejectedQuery = "select count("
	// builderTabInserted is what Tab on the empty line inserts: from and
	// the log's one table.
	builderTabInserted = "from STATS "
	// builderQueryRest is typed after builderTabInserted.
	builderQueryRest = "select count($line) group by $hostname"
	// builderQueryEcho is the builder's echo of the query it runs: the
	// Tab's insertion and the typed rest. It proves that the Tab inserted
	// the table: without it the typed rest alone would be the query, which
	// parses too (a query without a table) and prints the same table.
	builderQueryEcho = "query: " + builderTabInserted + builderQueryRest
	// missingQueryError is dmap's error for a missing query.
	missingQueryError = "no mapreduce query specified"
)

// writeBuilderLog writes builderLogFile and removes it with the test.
func writeBuilderLog(t *testing.T) {
	t.Helper()
	var lines []string
	for i := range builderStatsLines {
		lines = append(lines, fmt.Sprintf(guidedStatsLine, i%3, i))
	}
	if err := os.WriteFile(builderLogFile, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupFiles(t, builderLogFile)
}

// builderArgv returns the command line of a dmap without --query reading
// builderLogFile, against a new dserver or serverless, and the server's
// logs (nil serverless).
func builderArgv(ctx context.Context, t *testing.T, serverless bool) ([]string, *processOutputCollector) {
	t.Helper()
	argv := []string{"../dmap", "--cfg", "none", "--logger", "stdout", "--logLevel", "debug",
		"--noColor", "--files", builderLogFile}
	if serverless {
		return argv, nil
	}
	port, serverLogs := startInteractiveTestServer(ctx, t)
	return append(argv, "--servers", fmt.Sprintf("localhost:%d", port), "--trustAllHosts"), serverLogs
}

// TestDMapQueryBuilder runs dmap without --query on a PTY, against dserver
// and serverless: on a terminal the query builder opens by default (no
// --interactive-query), says so in its banner, and the files are probed as
// in an interactive dtail, so the first Tab inserts "from STATS " (the one
// table of the log). A query that does not parse is answered with the
// parse error and the builder stays open. The query built from the Tab's
// insertion and typed text runs as dmap --query does: the result table is
// printed and dmap exits by itself with status 0. Nothing is read before
// the query was entered.
func TestDMapQueryBuilder(t *testing.T) {
	skipIfNotIntegrationTest(t)
	for _, serverless := range []bool{false, true} {
		t.Run(fmt.Sprintf("serverless=%v", serverless), func(t *testing.T) {
			testDMapQueryBuilder(t, serverless)
		})
	}
}

func testDMapQueryBuilder(t *testing.T, serverless bool) {
	testLogger := NewTestLogger("TestDMapQueryBuilder")
	defer writeLogFileIgnoringError(testLogger)
	ctx, cancel := createTestContextWithTimeout(t)
	ctx = WithTestLogger(ctx, testLogger)
	defer cancel()
	writeBuilderLog(t)
	argv, serverLogs := builderArgv(ctx, t, serverless)

	// builderTabStep is the Tab; DTAIL_TEST_BUILDER_NO_TAB replaces it with
	// nothing, to show that the test fails when no Tab completes.
	builderTabStep := "\t"
	if os.Getenv("DTAIL_TEST_BUILDER_NO_TAB") != "" {
		builderTabStep = ""
	}
	raw := func(waitFor, input string) interactiveStep {
		return interactiveStep{WaitFor: waitFor, Input: input, Raw: true}
	}
	steps := []interactiveStep{
		raw(builderBannerPrefix, ""),
		// A query that does not parse keeps the builder open. It names no
		// table, so that the output holds "from STATS " only once the Tab
		// inserted it.
		raw(probeStoredMessage, builderRejectedQuery+"\r"),
		// The Tab starts the query with from and the log's one table.
		raw(builderRejected, builderTabStep),
		raw(builderTabInserted, builderQueryRest+"\r"),
		// The run ends dmap: nothing is sent once it started.
		raw(builderRunning, ""),
	}
	output, err := runInteractivePTYCommand(ctx, argv, steps)
	if err != nil {
		t.Fatalf("run dmap: %v\noutput:\n%s", err, output)
	}
	checkBuilderRun(t, output)
	if serverLogs == nil {
		return
	}
	// One session of the builder, which ran no command, and one of the
	// run, which ran the query: the map command reached the server once,
	// after the builder's session had ended.
	requireBuilderSessions(ctx, t, serverLogs, 2)
	logs := serverLogs.snapshot()
	if got := countSubstring(logs, serverMapCommand); got != 1 {
		t.Fatalf("map commands on the server = %d, want 1, logs:\n%s", got, serverLogs.dump())
	}
}

// Server log lines of the query builder tests: a client session's handler
// (the health check's is named otherwise), its end, and a map command.
const (
	serverSessionHandler = "Creating new server handler"
	serverSessionEnd     = "Good bye Mister!"
	serverMapCommand     = "[map:"
)

// requireBuilderSessions waits until want client sessions ended on the
// server (its log is written with a delay) and checks that no more were
// opened.
func requireBuilderSessions(ctx context.Context, t *testing.T, serverLogs *processOutputCollector, want int) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ended := func() int {
		count := 0
		for _, line := range serverLogs.snapshot() {
			if strings.Contains(line, serverSessionEnd) && !strings.Contains(line, "DTAIL-HEALTH") {
				count++
			}
		}
		return count
	}
	for ended() < want && waitCtx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if got := countSubstring(serverLogs.snapshot(), serverSessionHandler); ended() != want || got != want {
		t.Fatalf("server sessions: %d ended, %d opened, want %d, logs:\n%s", ended(), got, want, serverLogs.dump())
	}
}

// checkBuilderRun checks the output of a builder session that ran
// builderQueryRest on "from STATS": the result table follows the builder's
// acknowledgement and counts every line of the log, in one row.
func checkBuilderRun(t *testing.T, output string) {
	t.Helper()
	running := strings.Index(output, builderRunning)
	header := strings.Index(output, builderResultHeader)
	if running < 0 || header < running {
		t.Fatalf("no result table after %q:\n%s", builderRunning, output)
	}
	if !strings.Contains(output[running:header], builderQueryEcho) {
		t.Fatalf("the query run is not %q:\n%s", builderQueryEcho, output)
	}
	table := output[header+len(builderResultHeader):]
	if want := fmt.Sprintf(" %d \r\n", builderStatsLines); !strings.Contains(table, want) {
		t.Fatalf("result table lacks the row %q:\n%s", want, output)
	}
	if strings.Contains(output, missingQueryError) {
		t.Fatalf("dmap reported a missing query:\n%s", output)
	}
}

// TestDMapQueryBuilderQuit leaves the query builder with :quit, against
// dserver and serverless: dmap exits with status 0 and runs no query.
// :show before it reports the builder's state: the files, no workload.
func TestDMapQueryBuilderQuit(t *testing.T) {
	skipIfNotIntegrationTest(t)
	for _, serverless := range []bool{false, true} {
		t.Run(fmt.Sprintf("serverless=%v", serverless), func(t *testing.T) {
			ctx, cancel := createTestContextWithTimeout(t)
			defer cancel()
			writeBuilderLog(t)
			argv, serverLogs := builderArgv(ctx, t, serverless)
			steps := []interactiveStep{
				{WaitFor: builderBannerPrefix, Raw: true},
				// The session is up once its probe answered.
				{WaitFor: probeStoredMessage, Input: ":show"},
				{WaitFor: "workload=builder files=" + builderLogFile, Input: ":quit"},
			}
			output, err := runInteractivePTYCommand(ctx, argv, steps)
			if err != nil {
				t.Fatalf("run dmap: %v\noutput:\n%s", err, output)
			}
			for _, unwanted := range []string{builderRunning, builderResultHeader, missingQueryError} {
				if strings.Contains(output, unwanted) {
					t.Fatalf("output has %q after :quit:\n%s", unwanted, output)
				}
			}
			if serverLogs == nil {
				return
			}
			// The builder's session only, and no query ran on it.
			requireBuilderSessions(ctx, t, serverLogs, 1)
			if got := countSubstring(serverLogs.snapshot(), serverMapCommand); got != 0 {
				t.Fatalf("map commands on the server = %d, want none, logs:\n%s", got, serverLogs.dump())
			}
		})
	}
}

// TestDMapQueryBuilderStaysInteractive: with an explicit --interactive-query
// the query built in the builder runs as interactive dmap, against dserver
// and serverless: the result is printed and the session stays open (the
// interactive banner follows the builder's), Up recalls the built query
// from the builder's history, :show reports the running query, :query
// replaces it, and only :quit ends dmap, with status 0.
func TestDMapQueryBuilderStaysInteractive(t *testing.T) {
	skipIfNotIntegrationTest(t)
	for _, serverless := range []bool{false, true} {
		t.Run(fmt.Sprintf("serverless=%v", serverless), func(t *testing.T) {
			ctx, cancel := createTestContextWithTimeout(t)
			defer cancel()
			writeBuilderLog(t)
			argv, _ := builderArgv(ctx, t, serverless)
			argv = append(argv, "--interactive-query")
			steps := []interactiveStep{
				{WaitFor: builderBannerPrefix, Raw: true},
				{WaitFor: probeStoredMessage, Input: "from STATS " + builderQueryRest},
				{WaitFor: builderRunning, Raw: true},
				{WaitFor: interactiveBannerPrefix, Raw: true},
				// Up recalls the built query as the command replacing it.
				{WaitFor: builderResultHeader, Input: "\x1b[A", Raw: true},
				{WaitFor: ":query from STATS " + builderQueryRest, Input: "\x15:show\n", Raw: true},
				{WaitFor: "workload=query", Input: ":query from STATS select max(lifetimeConnections)"},
				{WaitFor: " max(lifetimeConnections) \r\n---", Input: ":quit"},
			}
			output, err := runInteractivePTYCommand(ctx, argv, steps)
			if err != nil {
				t.Fatalf("run dmap: %v\noutput:\n%s", err, output)
			}
			if banner := strings.Index(output, interactiveBannerPrefix); banner < strings.Index(output, builderRunning) {
				t.Fatalf("the interactive session did not follow the builder:\n%s", output)
			}
		})
	}
}

// TestDMapWithQueryOnTerminalRunsAtOnce: with a query, dmap on a terminal
// runs as before, whether the query is given with --query or as the
// positional argument the client setup takes for it: no builder opens, the
// result is printed and dmap exits with status 0.
func TestDMapWithQueryOnTerminalRunsAtOnce(t *testing.T) {
	skipIfNotIntegrationTest(t)
	const query = "from STATS select count($line) group by $hostname"
	for name, queryArgs := range map[string][]string{
		"--query":    {"--query", query, "--files", builderLogFile},
		"positional": {query, builderLogFile},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := createTestContextWithTimeout(t)
			defer cancel()
			writeBuilderLog(t)
			argv := append([]string{"../dmap", "--cfg", "none", "--logger", "stdout", "--noColor"}, queryArgs...)
			output, err := runInteractivePTYCommand(ctx, argv, nil)
			if err != nil {
				t.Fatalf("run dmap: %v\noutput:\n%s", err, output)
			}
			if strings.Contains(output, builderBannerPrefix) || !strings.Contains(output, builderResultHeader) {
				t.Fatalf("want the result table and no builder banner, output:\n%s", output)
			}
		})
	}
}

// TestDMapWithoutQueryNoTerminal: without a terminal (stdin and stdout are
// pipes here, as in a script or cron) a missing query stays dmap's error,
// with exit status 1, and so it does on a terminal with --no-interactive.
func TestDMapWithoutQueryNoTerminal(t *testing.T) {
	skipIfNotIntegrationTest(t)
	ctx, cancel := createTestContextWithTimeout(t)
	defer cancel()
	writeBuilderLog(t)
	argv := []string{"--cfg", "none", "--logger", "stdout", "--noColor", "--files", builderLogFile}

	t.Run("pipe", func(t *testing.T) {
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, "../dmap", argv...)
		cmd.Stdin = strings.NewReader("from STATS select count($line)\n")
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		requireMissingQueryExit(t, err, out.String())
	})
	t.Run("terminal with --no-interactive", func(t *testing.T) {
		output, err := runInteractivePTYCommand(ctx,
			append([]string{"../dmap", "--no-interactive"}, argv...), nil)
		requireMissingQueryExit(t, err, output)
	})
}

// requireMissingQueryExit checks that dmap exited with status 1 for the
// missing query (err is the run's error) and opened no builder.
func requireMissingQueryExit(t *testing.T, err error, output string) {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("dmap without a query: err %v, want exit status 1\noutput:\n%s", err, output)
	}
	if !strings.Contains(output, missingQueryError) || strings.Contains(output, builderBannerPrefix) {
		t.Fatalf("want the missing query error and no builder banner, output:\n%s", output)
	}
}
