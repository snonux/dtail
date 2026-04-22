//go:build !windows

package journaltest

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInstallMockEmitsScenarioAndRecordsParsedFlags(t *testing.T) {
	mock := InstallMock(t, Scenario{
		Default: Invocation{
			Lines:     []string{"default"},
			Stderr:    []string{"warning"},
			NoEntries: true,
		},
		Units: map[string]Invocation{
			"ssh.service": {
				Lines: []string{"alpha", "beta"},
			},
		},
	})

	cmd := exec.Command(journalctlCommand, "-u", "ssh.service", "-n", "2", "--output=json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run journalctl mock: %v", err)
	}

	if got, want := string(output), "alpha\nbeta\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got, want := strings.TrimSpace(mock.Args(t)), "-u ssh.service -n 2 --output=json"; got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
	if got := strings.TrimSpace(readOptionalFile(t, mock.UnitFile)); got != "ssh.service" {
		t.Fatalf("unit = %q, want ssh.service", got)
	}
	if got := strings.TrimSpace(readOptionalFile(t, mock.CountFile)); got != "2" {
		t.Fatalf("count = %q, want 2", got)
	}
	if got := strings.TrimSpace(readOptionalFile(t, mock.OutputFile)); got != "json" {
		t.Fatalf("output = %q, want json", got)
	}
}

func TestInstallMockSupportsErrorsPartialLongLinesAndDelay(t *testing.T) {
	mock := InstallMock(t, Scenario{
		Default: Invocation{
			Lines:          []string{"alpha"},
			PartialLine:    "partial",
			LongLineLength: 70 * 1024,
			InterLineDelay: 10 * time.Millisecond,
			ExitCode:       7,
			NoEntries:      true,
		},
	})

	started := time.Now()
	cmd := exec.Command(journalctlCommand)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("journalctl mock unexpectedly succeeded")
	}
	if exitCode := cmd.ProcessState.ExitCode(); exitCode != 7 {
		t.Fatalf("exit code = %d, want 7", exitCode)
	}
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("mock did not apply inter-line delay, elapsed %s", elapsed)
	}

	got := string(output)
	if !strings.Contains(got, "-- No entries --") {
		t.Fatalf("combined output missing no entries stderr: %q", got)
	}
	if !strings.Contains(got, "alpha\n") || !strings.Contains(got, "\npartial") {
		t.Fatalf("combined output missing regular or partial line: %q", got)
	}
	if !strings.Contains(got, strings.Repeat("x", 70*1024)) {
		t.Fatal("combined output missing long line")
	}
	if mock.Terminated(t) {
		t.Fatal("mock recorded SIGTERM without being signaled")
	}
}

func TestInstallMockFollowHonorsSIGTERM(t *testing.T) {
	mock := InstallMock(t, Scenario{
		Default: Invocation{
			FollowLines:    []string{"follow"},
			InterLineDelay: 20 * time.Millisecond,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, journalctlCommand, "-f", "-n", "0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("open stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start journalctl mock: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
	})

	buf := make([]byte, len("follow\n"))
	if _, err := io.ReadFull(stdout, buf); err != nil {
		t.Fatalf("read follow output: %v", err)
	}
	if string(buf) != "follow\n" {
		t.Fatalf("first follow output = %q, want follow", string(buf))
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal journalctl mock: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait after SIGTERM: %v", err)
	}

	mock.WaitForTerm(t, time.Second)
	if !strings.Contains(stderr.String(), TermSentinel) {
		t.Fatalf("stderr = %q, want term sentinel", stderr.String())
	}
	if got := strings.TrimSpace(readOptionalFile(t, mock.FollowFile)); got != "1" {
		t.Fatalf("follow flag = %q, want 1", got)
	}
}
