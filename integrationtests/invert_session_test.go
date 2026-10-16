package integrationtests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDGrepInteractiveInvertCarriesOver runs --invert through the interactive
// SESSION START/UPDATE path: the initial inverted regex comes from the
// command line, :show reports it, a :reload of the regex alone keeps it (so
// the inverted empty pattern prints no line), and --invert=false matches
// every line again. Each data line is counted at the end: the inverted
// "alpha" read prints the other two lines, the inverted empty read prints
// nothing, and the final positive empty read prints all three once more.
func TestDGrepInteractiveInvertCarriesOver(t *testing.T) {
	skipIfNotIntegrationTest(t)

	testLogger := NewTestLogger("TestDGrepInteractiveInvertCarriesOver")
	defer writeLogFileIgnoringError(testLogger)
	cleanupTmpFiles(t)

	ctx, cancel := createTestContextWithTimeout(t)
	ctx = WithTestLogger(ctx, testLogger)
	defer cancel()

	inputFile := "interactive_dgrep_invert.tmp"
	if err := os.WriteFile(inputFile, []byte("alpha line\nbeta line\ngamma line\n"), 0600); err != nil {
		t.Fatalf("unable to create input file: %v", err)
	}
	cleanupFiles(t, inputFile)

	port, _ := startInteractiveTestServer(ctx, t)
	clientOutput, err := runInteractivePTYCommand(ctx, []string{
		"../dgrep", "--cfg", "none", "--logger", "stdout", "--logLevel", "info",
		"--servers", fmt.Sprintf("localhost:%d", port), "--files", inputFile,
		"--grep", "alpha", "--invert", "--plain", "--trustAllHosts", "--interactive-query",
	}, []interactiveStep{
		{WaitFor: "gamma line", Input: ":show"},
		{WaitFor: `regex="alpha" invert=true`, Input: ":reload --regex ''"},
		{WaitFor: "reload applied successfully", Input: ":show"},
		{WaitFor: `regex="" invert=true`, Input: ":reload --regex '' --invert=false"},
		{WaitFor: "alpha line", Input: ":show"},
		{WaitFor: `regex="" invert=false`, Input: ":quit"},
	})
	if err != nil {
		t.Fatalf("run interactive dgrep: %v\noutput:\n%s", err, clientOutput)
	}
	for needle, want := range map[string]int{
		"alpha line":                  1,
		"beta line":                   2,
		"gamma line":                  2,
		"reload applied successfully": 2,
	} {
		if got := strings.Count(clientOutput, needle); got != want {
			t.Fatalf("expected %d x %q in output, got %d:\n%s", want, needle, got, clientOutput)
		}
	}
}

// TestDTailInvertEmptyRegex follows a file with dtail, an empty --regex and
// --invert in the default command mode, where the client sends the read command
// "regex:invert,literal " that dserver splits at single spaces: it must print
// no line. A second client inverting a regex that never matches runs as a
// control and must print the appended lines, which proves they reached the
// follow sessions.
func TestDTailInvertEmptyRegex(t *testing.T) {
	skipIfNotIntegrationTest(t)

	testLogger := NewTestLogger("TestDTailInvertEmptyRegex")
	defer writeLogFileIgnoringError(testLogger)
	cleanupTmpFiles(t)

	ctx, cancel := createTestContextWithTimeout(t)
	ctx = WithTestLogger(ctx, testLogger)
	defer cancel()

	followFile := "dtail_invert_empty.follow.tmp"
	if err := os.WriteFile(followFile, nil, 0600); err != nil {
		t.Fatalf("unable to create follow file: %v", err)
	}
	cleanupFiles(t, followFile)

	port, serverLogs := startInteractiveTestServer(ctx, t)
	inverted := startDTailFollowClient(ctx, t, port, followFile, "")
	control := startDTailFollowClient(ctx, t, port, followFile, "never-matching-regex")
	if err := waitForCollectorCount(ctx, serverLogs, "Start reading", 2); err != nil {
		t.Fatalf("wait for both follow reads: %v\nserver logs:\n%s", err, serverLogs.dump())
	}

	const needle = "invert-follow-line"
	for i := 0; countSubstring(control.snapshot(), needle) < 5; i++ {
		if err := appendLines(followFile, fmt.Sprintf("%s %d", needle, i)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("control client got no lines:\n%s", control.dump())
		case <-time.After(200 * time.Millisecond):
		}
	}
	// Give the inverted client the time of a few more lines to print what a
	// broken inverted empty pattern (a noop) would print.
	time.Sleep(time.Second)
	if got := countSubstring(inverted.snapshot(), needle); got != 0 {
		t.Fatalf("dtail --regex '' --invert printed %d lines, want none:\n%s", got, inverted.dump())
	}
}

// startDTailFollowClient starts a dtail follow client of file with --invert
// and the regex, and returns the collector of its output lines.
func startDTailFollowClient(ctx context.Context, t *testing.T, port int, file, regex string) *processOutputCollector {
	t.Helper()
	stdoutCh, stderrCh, _, err := startCommand(ctx, t, "", "../dtail",
		"--cfg", "none", "--logger", "stdout", "--logLevel", "info",
		"--servers", fmt.Sprintf("localhost:%d", port), "--files", file,
		"--regex", regex, "--invert", "--trustAllHosts", "--noColor",
	)
	if err != nil {
		t.Fatal(err)
	}
	return startProcessOutputCollector(ctx, stdoutCh, stderrCh)
}
