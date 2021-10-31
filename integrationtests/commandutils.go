package integrationtests

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func runCommand(ctx context.Context, t *testing.T, stdoutFile, cmdStr string,
	args ...string) (int, error) {

	if _, err := os.Stat(cmdStr); err != nil {
		return 0, fmt.Errorf("no such executable '%s', please compile first: %v", cmdStr, err)
	}

	t.Log("Creating stdout file", stdoutFile)
	fd, err := os.Create(stdoutFile)
	if err != nil {
		return 0, nil
	}
	defer fd.Close()

	t.Log("Running command", cmdStr, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, cmdStr, args...)
	out, err := cmd.CombinedOutput()
	t.Log("Done running command!", err)
	fd.Write(out)

	return exitCodeFromError(err), err
}

func runCommandRetry(ctx context.Context, t *testing.T, retries int, stdoutFile,
	cmd string, args ...string) (exitCode int, err error) {

	for i := 0; i < retries; i++ {
		time.Sleep(time.Second)
		if exitCode, err = runCommand(ctx, t, stdoutFile, cmd, args...); exitCode == 0 {
			return
		}
	}
	return
}

func startCommand(ctx context.Context, t *testing.T, inPipeFile,
	cmdStr string, args ...string) (<-chan string, <-chan string, <-chan error, error) {

	stdoutCh := make(chan string)
	stderrCh := make(chan string)

	if _, err := os.Stat(cmdStr); err != nil {
		return stdoutCh, stderrCh, nil,
			fmt.Errorf("no such executable '%s', please compile first: %v", cmdStr, err)
	}

	t.Log(cmdStr, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, cmdStr, args...)

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return stdoutCh, stderrCh, nil, err
	}

	cmdStderr, err := cmd.StderrPipe()
	err = cmd.Start()
	if err != nil {
		return stdoutCh, stderrCh, nil, err
	}

	// Read input file and send to stdin pipe?
	if inPipeFile != "" {
		t.Log(fmt.Sprintf("Piping %s to stdin pipe", inPipeFile))
		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			return stdoutCh, stderrCh, nil, err
		}
		fd, err := os.Open(inPipeFile)
		if err != nil {
			return stdoutCh, stderrCh, nil, err
		}
		defer fd.Close()
		go io.Copy(stdinPipe, bufio.NewReader(fd))
	}

	go func() {
		scanner := bufio.NewScanner(cmdStdout)
		scanner.Split(bufio.ScanLines)
		for scanner.Scan() {
			stdoutCh <- scanner.Text()
		}
	}()
	go func() {
		scanner := bufio.NewScanner(cmdStderr)
		scanner.Split(bufio.ScanLines)
		for scanner.Scan() {
			stderrCh <- scanner.Text()
		}
		close(stderrCh)
	}()

	cmdErrCh := make(chan error)
	go func() {
		cmdErrCh <- cmd.Wait()
	}()

	return stdoutCh, stderrCh, cmdErrCh, nil
}

func waitForCommand(ctx context.Context, t *testing.T,
	stdoutCh, stderrCh <-chan string, cmdErrCh <-chan error) {

	for {
		select {
		case line, ok := <-stdoutCh:
			if ok {
				t.Log(line)
			}
		case line, ok := <-stderrCh:
			if ok {
				t.Log(line)
			}
		case cmdErr := <-cmdErrCh:
			t.Log(fmt.Sprintf("Command finished with with exit code %d: %v",
				exitCodeFromError(cmdErr), cmdErr))
			return
		}
	}
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	if exitError, ok := err.(*exec.ExitError); ok {
		ws := exitError.Sys().(syscall.WaitStatus)
		return ws.ExitStatus()
	}
	panic(fmt.Sprintf("Unable to get process exit code from error: %v", err))
}
