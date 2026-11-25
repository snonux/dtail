package integrationtests

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

// progressRun is one client command of TestReadProgress.
type progressRun struct {
	name string
	cmd  string
	args []string
}

// TestReadProgress runs dcat, dgrep and dmap with --plain --progress over a
// plain and a gzip log, against dserver and serverless. The read progress
// goes to stderr: its last PROGRESS line must count every byte of both files
// on disk (the gzip one compressed), and stdout must hold no PROGRESS line
// and equal the output of the same run without --progress.
func TestReadProgress(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}
	files, size := writeProgressInputs(t)
	query := "from STATS select count($line),max($goroutines) group by $hostname"
	runs := []progressRun{
		{"dcat", "../dcat", []string{"--files", strings.Join(files, ",")}},
		{"dgrep", "../dgrep", []string{"--regex", "STATS", "--files", strings.Join(files, ",")}},
		{"dmap", "../dmap", []string{"--query", query, "--files", strings.Join(files, ",")}},
	}
	want := fmt.Sprintf("|bytes=%d|total=%d", size, size)

	for _, serverMode := range []bool{false, true} {
		var serverArgs []string
		name := "Serverless"
		if serverMode {
			name = "ServerMode"
			serverArgs = startProgressDServer(t)
		}
		for _, run := range runs {
			t.Run(name+"/"+run.name, func(t *testing.T) {
				args := append([]string{"--plain", "--cfg", "none", "--noColor"}, serverArgs...)
				args = append(args, run.args...)
				plainOut, _ := runSplitOutput(t, run.cmd, args...)
				stdout, stderr := runSplitOutput(t, run.cmd, append([]string{"--progress"}, args...)...)
				checkProgressOutput(t, stderr, want)
				if strings.Contains(stdout, "PROGRESS|") {
					t.Fatalf("PROGRESS line on stdout:\n%s", stdout)
				}
				// The two files are read concurrently, so their lines
				// interleave differently from run to run.
				if run.name != "dmap" && !sameLines(stdout, plainOut) {
					t.Fatalf("stdout with --progress differs from the run without it")
				}
			})
		}
	}
}

// sameLines reports whether a and b hold the same lines in any order.
func sameLines(a, b string) bool {
	linesA, linesB := strings.Split(a, "\n"), strings.Split(b, "\n")
	slices.Sort(linesA)
	slices.Sort(linesB)
	return slices.Equal(linesA, linesB)
}

// checkProgressOutput checks that the last PROGRESS line of stderr ends with
// want (the byte counts) and says the read is complete, with a full bar.
func checkProgressOutput(t *testing.T, stderr, want string) {
	t.Helper()
	var last string
	for line := range strings.SplitSeq(stderr, "\n") {
		if strings.HasPrefix(line, "PROGRESS|") {
			last = line
		}
	}
	for _, field := range []string{"PROGRESS|bar=[####################]|progress%=100.0|",
		"|files=2/2|", "|reporting=1/1|", "|eta=0s|"} {
		if !strings.HasSuffix(last, want) || !strings.Contains(last, field) {
			t.Fatalf("last PROGRESS line %q, want suffix %q and %q; stderr:\n%s", last, want, field, stderr)
		}
	}
}

// writeProgressInputs writes a copy of the MapReduce test data and a gzip
// copy of it and returns their paths and summed size on disk.
func writeProgressInputs(t *testing.T) ([]string, int64) {
	t.Helper()
	data, err := os.ReadFile(GetStandardTestPaths().MaprTestData)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "progress.log")
	if err := os.WriteFile(plain, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	gz := filepath.Join(dir, "progress.log.gz")
	if err := os.WriteFile(gz, compressed.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{plain, gz}, int64(len(data) + compressed.Len())
}

// startProgressDServer starts a dserver for the test and returns the client
// arguments to connect to it.
func startProgressDServer(t *testing.T) []string {
	t.Helper()
	port := getUniquePortNumber()
	bindAddress := "localhost"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, _, _, err := startCommand(ctx, t, "", "../dserver",
		"--cfg", "none", "--logger", "stdout", "--logLevel", "error",
		"--bindAddress", bindAddress, "--port", fmt.Sprintf("%d", port))
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForServerReady(ctx, bindAddress, port); err != nil {
		t.Fatal(err)
	}
	return []string{"--servers", fmt.Sprintf("%s:%d", bindAddress, port), "--trustAllHosts"}
}

// runSplitOutput runs a client and returns its stdout and stderr apart.
func runSplitOutput(t *testing.T, cmdStr string, args ...string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cmdStr, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\nstdout:\n%s\nstderr:\n%s", cmdStr, strings.Join(args, " "), err,
			stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}
