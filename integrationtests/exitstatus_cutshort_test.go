package integrationtests

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

// cutShortLines is the number of lines of the file the cut short tests read,
// about 40 MB: much more than the buffers between dserver and the test hold
// (the session output buffer, the SSH channel window, the socket and pipe
// buffers), so that the read is still running when the test, which stops
// reading the client's output after its first payload line, stops dserver or
// the client.
const cutShortLines = 400000

// cutShortError is the error dcat and dgrep log for a session cut short (see
// clients.baseClient.reportCutShort).
const cutShortError = "Session ended without the server's close handshake"

// cutShortRun is the result of runCutShortClient.
type cutShortRun struct {
	exitCode int
	lines    int    // payload lines of the file in the output
	output   string // the other lines of the output
}

// cutShortStop stops a run once its first payload line arrived: killServer
// SIGKILLs the dserver and waits until it exited, client is the client's
// process, and clientDone is closed once the client's output ended.
type cutShortStop func(killServer func(), client *os.Process, clientDone <-chan struct{})

// TestDCatDGrepExitStatusCutShort checks that dcat and dgrep exit with status
// 1 when dserver is SIGKILLed in the middle of a read, so that the session
// ends without the server's close handshake, with --plain and with framed
// output, that the same read exits with status 0 when it completes, and that
// a client stopped by SIGTERM or a double SIGINT keeps exit status 0.
func TestDCatDGrepExitStatusCutShort(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}
	testLogger := NewTestLogger("TestDCatDGrepExitStatusCutShort")
	defer writeLogFileIgnoringError(testLogger)
	file := writeCutShortFile(t)

	for _, tc := range cutShortCommands(file) {
		t.Run(tc.name+", server killed", func(t *testing.T) {
			run := runCutShortClient(t, testLogger, tc.cmd, tc.args, killCutShortServer)
			if run.exitCode != 1 || run.lines >= cutShortLines || !strings.Contains(run.output, cutShortError) {
				t.Fatalf("%s exit status = %d with %d of %d lines, want 1 with fewer lines and %q; output:\n%s",
					tc.cmd, run.exitCode, run.lines, cutShortLines, cutShortError, run.output)
			}
		})
	}
	dcatArgs := []string{"--plain", "--files", file}
	t.Run("dcat completed", func(t *testing.T) {
		run := runCutShortClient(t, testLogger, "../dcat", dcatArgs, nil)
		if run.exitCode != 0 || run.lines != cutShortLines {
			t.Fatalf("dcat exit status = %d with %d of %d lines, want 0 with all lines; output:\n%s",
				run.exitCode, run.lines, cutShortLines, run.output)
		}
	})
	for name, stop := range map[string]cutShortStop{"SIGTERM": stopCutShortClientTerm,
		"double SIGINT": stopCutShortClientInterrupt} {
		t.Run("dcat stopped by "+name, func(t *testing.T) {
			run := runCutShortClient(t, testLogger, "../dcat", dcatArgs, stop)
			t.Logf("dcat read %d of %d lines", run.lines, cutShortLines)
			if run.exitCode != 0 || strings.Contains(run.output, cutShortError) {
				t.Fatalf("dcat exit status = %d, want 0 and no %q; output:\n%s",
					run.exitCode, cutShortError, run.output)
			}
		})
	}
}

// cutShortCommands are the dcat and dgrep runs reading file whose server the
// test kills.
func cutShortCommands(file string) []exitStatusCase {
	dgrep := []string{"--grep", "exit status", "--files", file}
	return []exitStatusCase{
		{name: "dcat plain", cmd: "../dcat", args: []string{"--plain", "--files", file}},
		{name: "dcat framed", cmd: "../dcat", args: []string{"--files", file}},
		{name: "dgrep plain", cmd: "../dgrep", args: append([]string{"--plain"}, dgrep...)},
		{name: "dgrep framed", cmd: "../dgrep", args: dgrep},
	}
}

// writeCutShortFile writes the file of cutShortLines lines the tests read.
func writeCutShortFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cutshort.log")
	fd, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriter(fd)
	padding := strings.Repeat("x", 64)
	for i := 1; i <= cutShortLines; i++ {
		if _, err := fmt.Fprintf(writer, "%s %07d %s\n", exitStatusLine, i, padding); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := fd.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// killCutShortServer SIGKILLs the dserver while it reads.
func killCutShortServer(killServer func(), _ *os.Process, _ <-chan struct{}) {
	killServer()
}

// stopCutShortClientTerm sends the client SIGTERM.
func stopCutShortClientTerm(_ func(), client *os.Process, _ <-chan struct{}) {
	_ = client.Signal(syscall.SIGTERM)
}

// stopCutShortClientInterrupt presses Ctrl+C twice: it sends the client two
// SIGINTs, the second within the interrupt pause (config.InterruptTimeoutS)
// that the first one starts, which stops the client. The client ignores a
// first SIGINT while its stats loop is not ready to print the Ctrl+C hint,
// so the pair is sent again if the client's output did not end within 10
// seconds. The client exits about 3 seconds after the pair (the hint pauses
// its output for the interrupt pause), and at the latest 5 seconds after it
// (signal.forceExitAfter); a SIGINT after it stopped listening would kill it,
// so the wait is longer than both.
func stopCutShortClientInterrupt(_ func(), client *os.Process, clientDone <-chan struct{}) {
	go func() {
		for {
			_ = client.Signal(os.Interrupt)
			time.Sleep(200 * time.Millisecond)
			_ = client.Signal(os.Interrupt)
			select {
			case <-clientDone:
				return
			case <-time.After(10 * time.Second):
			}
		}
	}()
}

// runCutShortClient starts a dserver and cmdStr with args reading from it,
// calls stop (if not nil) once the first payload line arrived and the
// client's output is held back, then reads the rest of the output and waits
// for the client to exit.
func runCutShortClient(t *testing.T, logger *TestLogger, cmdStr string, args []string,
	stop cutShortStop) cutShortRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(WithTestLogger(context.Background(), logger), 2*time.Minute)
	defer cancel()
	serverCtx, cancelServer := context.WithCancel(ctx)
	port := getUniquePortNumber()
	exited := startServerB(serverCtx, t, "none", port, "", nil)
	killServer := func() { cancelServer(); <-exited }
	defer killServer()
	if err := waitForServerReady(ctx, "127.0.0.1", port); err != nil {
		t.Fatal(err)
	}

	args = append([]string{"--cfg", "none", "--logLevel", "error", "--noColor",
		"--servers", fmt.Sprintf("127.0.0.1:%d", port), "--trustAllHosts"}, args...)
	cmd, output := startCutShortClient(ctx, t, cmdStr, args)
	reader := bufio.NewReader(output)
	var run cutShortRun
	for run.lines == 0 {
		line, err := reader.ReadString('\n')
		run.add(line)
		if err != nil {
			t.Fatalf("%s output ended before its first line: %v; output:\n%s", cmdStr, err, run.output)
		}
	}
	clientDone := make(chan struct{})
	if stop != nil {
		stop(killServer, cmd.Process, clientDone)
	}
	run.readAll(t, reader)
	close(clientDone)
	run.exitCode = exitCodeFromError(cmd.Wait())
	return run
}

// startCutShortClient starts the client with its stdout and stderr on a
// pipe, which it returns to read from.
func startCutShortClient(ctx context.Context, t *testing.T, cmdStr string, args []string) (*exec.Cmd, io.Reader) {
	t.Helper()
	if logger := GetTestLogger(ctx); logger != nil {
		logger.LogCommand(cmdStr, args)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	cmd := exec.CommandContext(ctx, cmdStr, args...)
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return cmd, reader
}

// add counts line if it is a payload line of the file and keeps it otherwise.
func (r *cutShortRun) add(line string) {
	if strings.Contains(line, exitStatusLine) {
		r.lines++
	} else if line != "" {
		r.output += line
	}
}

// readAll reads the rest of the client's output, until the client exited.
func (r *cutShortRun) readAll(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		r.add(line)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("read client output: %v", err)
		}
	}
}
