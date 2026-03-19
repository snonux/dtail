package integrationtests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type interactiveStep struct {
	Delay time.Duration
	Input string
}

type processOutputCollector struct {
	mu    sync.Mutex
	lines []string
}

func TestDTailInteractiveReloadReusesSessionAndDropsLateOldMatches(t *testing.T) {
	skipIfNotIntegrationTest(t)

	testLogger := NewTestLogger("TestDTailInteractiveReloadReusesSessionAndDropsLateOldMatches")
	defer testLogger.WriteLogFile()
	cleanupTmpFiles(t)

	ctx, cancel := createTestContextWithTimeout(t)
	ctx = WithTestLogger(ctx, testLogger)
	defer cancel()

	followFile := "interactive_dtail_reload.tmp"
	if err := os.WriteFile(followFile, nil, 0600); err != nil {
		t.Fatalf("unable to create follow file: %v", err)
	}
	cleanupFiles(t, followFile, "interactive_dtail_reload.stdout.tmp")

	port := getUniquePortNumber()
	serverStdout, serverStderr, _, err := startCommand(ctx, t, "", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "debug",
		"--bindAddress", "localhost",
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Fatalf("start dserver: %v", err)
	}
	serverLogs := startProcessOutputCollector(ctx, serverStdout, serverStderr)
	if err := waitForServerReady(ctx, "localhost", port); err != nil {
		t.Fatalf("wait for dserver: %v", err)
	}
	serverLogs.reset()

	writerDone := make(chan error, 1)
	go func() {
		if err := waitForCollectorSubstring(ctx, serverLogs, "Start reading|"+followFile+"|"+followFile); err != nil {
			writerDone <- err
			return
		}
		writerDone <- appendLinesOnSchedule(ctx, followFile, []interactiveStep{
			{Delay: 100 * time.Millisecond, Input: "ERROR initial"},
			{Delay: 3000 * time.Millisecond, Input: "ERROR late"},
			{Delay: 3200 * time.Millisecond, Input: "WARN live"},
		})
	}()

	clientOutput, err := runInteractivePTYCommand(ctx, []string{
		"../dtail",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "info",
		"--servers", fmt.Sprintf("localhost:%d", port),
		"--files", followFile,
		"--grep", "ERROR",
		"--plain",
		"--trustAllHosts",
		"--interactive-query",
	}, []interactiveStep{
		{Delay: 3 * time.Second, Input: ":reload --grep WARN"},
		{Delay: 6 * time.Second, Input: ":quit"},
	})
	if err != nil {
		t.Fatalf("run interactive dtail: %v\noutput:\n%s", err, clientOutput)
	}

	if err := <-writerDone; err != nil {
		t.Fatalf("write follow file: %v", err)
	}

	if !strings.Contains(clientOutput, "ERROR initial") {
		t.Fatalf("expected initial ERROR line in output:\n%s", clientOutput)
	}
	if !strings.Contains(clientOutput, "WARN live") {
		t.Fatalf("expected WARN line after reload in output:\n%s", clientOutput)
	}
	if strings.Contains(clientOutput, "ERROR late") {
		t.Fatalf("unexpected stale ERROR line after reload:\n%s", clientOutput)
	}
	if !strings.Contains(clientOutput, "reload applied successfully") {
		t.Fatalf("expected reload success message in output:\n%s", clientOutput)
	}
	if countSubstring(serverLogs.snapshot(), "Creating new server handler") != 1 {
		t.Fatalf("expected one SSH session on the server, logs:\n%s", strings.Join(serverLogs.snapshot(), "\n"))
	}
}

func TestDGrepInteractiveReloadReusesSessionAfterCompletedRead(t *testing.T) {
	skipIfNotIntegrationTest(t)

	testLogger := NewTestLogger("TestDGrepInteractiveReloadReusesSessionAfterCompletedRead")
	defer testLogger.WriteLogFile()
	cleanupTmpFiles(t)

	ctx, cancel := createTestContextWithTimeout(t)
	ctx = WithTestLogger(ctx, testLogger)
	defer cancel()

	inputFile := "interactive_dgrep_reload.tmp"
	if err := os.WriteFile(inputFile, []byte("ERROR first\nWARN second\n"), 0600); err != nil {
		t.Fatalf("unable to create input file: %v", err)
	}
	cleanupFiles(t, inputFile, "interactive_dgrep_reload.stdout.tmp")

	port := getUniquePortNumber()
	serverStdout, serverStderr, _, err := startCommand(ctx, t, "", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "debug",
		"--bindAddress", "localhost",
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Fatalf("start dserver: %v", err)
	}
	serverLogs := startProcessOutputCollector(ctx, serverStdout, serverStderr)
	if err := waitForServerReady(ctx, "localhost", port); err != nil {
		t.Fatalf("wait for dserver: %v", err)
	}
	serverLogs.reset()

	clientOutput, err := runInteractivePTYCommand(ctx, []string{
		"../dgrep",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "info",
		"--servers", fmt.Sprintf("localhost:%d", port),
		"--files", inputFile,
		"--grep", "ERROR",
		"--plain",
		"--trustAllHosts",
		"--interactive-query",
	}, []interactiveStep{
		{Delay: 3500 * time.Millisecond, Input: ":reload --grep WARN"},
		{Delay: 5500 * time.Millisecond, Input: ":quit"},
	})
	if err != nil {
		t.Fatalf("run interactive dgrep: %v\noutput:\n%s", err, clientOutput)
	}

	if !strings.Contains(clientOutput, "ERROR first") {
		t.Fatalf("expected initial grep result in output:\n%s", clientOutput)
	}
	if !strings.Contains(clientOutput, "WARN second") {
		t.Fatalf("expected reloaded grep result in output:\n%s", clientOutput)
	}
	if !strings.Contains(clientOutput, "reload applied successfully") {
		t.Fatalf("expected reload success message in output:\n%s", clientOutput)
	}
	if countSubstring(serverLogs.snapshot(), "Creating new server handler") != 1 {
		t.Fatalf("expected one SSH session on the server, logs:\n%s", strings.Join(serverLogs.snapshot(), "\n"))
	}
}

func startProcessOutputCollector(ctx context.Context, stdoutCh, stderrCh <-chan string) *processOutputCollector {
	collector := &processOutputCollector{}
	collect := func(ch <-chan string) {
		for {
			select {
			case line, ok := <-ch:
				if !ok {
					return
				}
				collector.append(line)
			case <-ctx.Done():
				return
			}
		}
	}
	go collect(stdoutCh)
	go collect(stderrCh)
	return collector
}

func (c *processOutputCollector) append(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
}

func (c *processOutputCollector) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

func (c *processOutputCollector) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = c.lines[:0]
}

func appendLinesOnSchedule(ctx context.Context, path string, steps []interactiveStep) error {
	fd, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer fd.Close()

	start := time.Now()
	for _, step := range steps {
		wait := time.Until(start.Add(step.Delay))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}

		if _, err := fd.WriteString(step.Input + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func runInteractivePTYCommand(ctx context.Context, argv []string, steps []interactiveStep) (string, error) {
	if _, err := exec.LookPath("python3"); err != nil {
		return "", fmt.Errorf("python3 is required for PTY integration tests: %w", err)
	}

	script := `
import json
import os
import pty
import sys
import threading
import time

argv = json.loads(os.environ["DTAIL_PTY_ARGV"])
steps = json.loads(os.environ["DTAIL_PTY_STEPS"])

pid, fd = pty.fork()
if pid == 0:
    os.execv(argv[0], argv)

def writer():
    for step in steps:
        time.sleep(step["delay_ms"] / 1000.0)
        data = step["input"]
        if not data.endswith("\n"):
            data += "\n"
        os.write(fd, data.encode("utf-8"))

threading.Thread(target=writer, daemon=True).start()

output = bytearray()
while True:
    try:
        chunk = os.read(fd, 4096)
    except OSError:
        break
    if not chunk:
        break
    output.extend(chunk)

_, status = os.waitpid(pid, 0)
sys.stdout.buffer.write(output)
if os.WIFEXITED(status):
    sys.exit(os.WEXITSTATUS(status))
if os.WIFSIGNALED(status):
    sys.exit(128 + os.WTERMSIG(status))
sys.exit(1)
`

	encodedSteps := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		encodedSteps = append(encodedSteps, map[string]any{
			"delay_ms": step.Delay.Milliseconds(),
			"input":    step.Input,
		})
	}

	argvPayload, err := json.Marshal(argv)
	if err != nil {
		return "", err
	}
	stepsPayload, err := json.Marshal(encodedSteps)
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, "python3", "-c", script)
	cmd.Env = append(os.Environ(),
		"DTAIL_PTY_ARGV="+string(argvPayload),
		"DTAIL_PTY_STEPS="+string(stepsPayload),
	)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func countSubstring(lines []string, needle string) int {
	count := 0
	for _, line := range lines {
		if strings.Contains(line, needle) {
			count++
		}
	}
	return count
}

func waitForCollectorSubstring(ctx context.Context, collector *processOutputCollector, needle string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		for _, line := range collector.snapshot() {
			if strings.Contains(line, needle) {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
