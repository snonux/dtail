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

	var stdinPipe io.WriteCloser
	if inPipeFile != "" {
		var err error
		stdinPipe, err = cmd.StdinPipe()
		if err != nil {
			return stdoutCh, stderrCh, nil, err
		}
	}
	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return stdoutCh, stderrCh, nil, err
	}
	cmdStderr, err := cmd.StderrPipe()
	if err != nil {
		return stdoutCh, stderrCh, nil, err
	}
	err = cmd.Start()
	if err != nil {
		return stdoutCh, stderrCh, nil, err
	}

	// Read input file and send to stdin pipe?
	if inPipeFile != "" {
		t.Log(fmt.Sprintf("Piping %s to stdin pipe", inPipeFile))
		fd, err := os.Open(inPipeFile)
		if err != nil {
			return stdoutCh, stderrCh, nil, err
		}
		go func() {
			io.Copy(stdinPipe, bufio.NewReader(fd))
			stdinPipe.Close()
			fd.Close()
		}()
	}

	go func() {
		// Read raw bytes to preserve line endings, but filter out protocol messages
		buf := make([]byte, 4096)
		var accumulated []byte
		
		for {
			n, err := cmdStdout.Read(buf)
			if n > 0 {
				accumulated = append(accumulated, buf[:n]...)
				
				// Process accumulated data line by line, preserving original line endings
				data := accumulated
				var processedLines [][]byte
				var remaining []byte
				
				// Split on LF while preserving CRLF sequences
				start := 0
				for i := 0; i < len(data); i++ {
					if data[i] == '\n' {
						// Found a complete line (including the \n)
						line := data[start:i+1]
						processedLines = append(processedLines, line)
						start = i + 1
					}
				}
				
				// Keep any remaining partial line
				if start < len(data) {
					remaining = data[start:]
				}
				accumulated = remaining
				
				// Send complete lines, filtering out protocol messages
				for _, line := range processedLines {
					lineStr := string(line)
					lineContent := strings.TrimRight(lineStr, "\r\n")
					
					// Filter out protocol messages like ".syn close connection"
					if strings.HasPrefix(lineContent, ".syn ") || 
					   strings.HasPrefix(lineContent, "CLIENT|") ||
					   strings.HasPrefix(lineContent, "SERVER|") {
						continue
					}
					
					// Check for protocol messages appended to content (like "content.syn close connection")
					if strings.Contains(lineContent, ".syn close connection") {
						// Remove the protocol message from the content
						cleanContent := strings.Replace(lineContent, ".syn close connection", "", 1)
						// Preserve the original line ending
						lineEnding := lineStr[len(lineContent):]
						stdoutCh <- cleanContent + lineEnding
					} else {
						stdoutCh <- lineStr
					}
				}
			}
			if err != nil {
				// Send any remaining data in buffer
				if len(accumulated) > 0 {
					remaining := string(accumulated)
					remainingContent := strings.TrimRight(remaining, "\r\n")
					// Filter out protocol messages
					if strings.HasPrefix(remainingContent, ".syn ") || 
					   strings.HasPrefix(remainingContent, "CLIENT|") ||
					   strings.HasPrefix(remainingContent, "SERVER|") {
						// Skip protocol messages
					} else if strings.Contains(remainingContent, ".syn close connection") {
						// Remove the protocol message from the content
						cleanContent := strings.Replace(remainingContent, ".syn close connection", "", 1)
						// Preserve the original ending
						ending := remaining[len(remainingContent):]
						stdoutCh <- cleanContent + ending
					} else {
						stdoutCh <- remaining
					}
				}
				break
			}
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
