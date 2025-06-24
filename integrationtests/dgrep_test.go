package integrationtests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

func TestDGrep1(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrep1Serverless(t)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrep1WithServer(t)
	})
}

func testDGrep1Serverless(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep.stdout.tmp"
	expectedOutFile := "dgrep1.txt.expected"

	_, err := runCommand(context.TODO(), t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func testDGrep1WithServer(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep.stdout.tmp"
	expectedOutFile := "dgrep1.txt.expected"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start dserver
	_, _, _, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "error",
		"--bindAddress", bindAddress,
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Error(err)
		return
	}

	// Give server time to start
	time.Sleep(500 * time.Millisecond)

	_, err = runCommand(ctx, t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
		"--files", inFile)

	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func TestDGrep1Colors(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrep1ColorsServerless(t)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrep1ColorsWithServer(t)
	})
}

func testDGrep1ColorsServerless(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep1colors.stdout.tmp"

	// Run without --plain to get colored output
	_, err := runCommand(context.TODO(), t, outFile,
		"../dgrep",
		"--cfg", "none",
		"--grep", "1002-071947",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	// Verify it ran successfully and produced output
	info, err := os.Stat(outFile)
	if err != nil {
		t.Error("Output file not created:", err)
		return
	}
	if info.Size() == 0 {
		t.Error("Output file is empty")
		return
	}

	// Verify output contains ANSI color codes
	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Error("Failed to read output file:", err)
		return
	}
	if !strings.Contains(string(content), "\033[") {
		t.Error("Output does not contain ANSI color codes")
		return
	}

	os.Remove(outFile)
}

func testDGrep1ColorsWithServer(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep1colors.stdout.tmp"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start dserver
	_, _, _, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "error",
		"--bindAddress", bindAddress,
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Error(err)
		return
	}

	// Give server time to start
	time.Sleep(500 * time.Millisecond)

	// Run without --plain and without --noColor to get colored output
	_, err = runCommand(ctx, t, outFile,
		"../dgrep",
		"--cfg", "none",
		"--grep", "1002-071947",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--files", inFile)

	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	// Verify it ran successfully and produced output
	info, err := os.Stat(outFile)
	if err != nil {
		t.Error("Output file not created:", err)
		return
	}
	if info.Size() == 0 {
		t.Error("Output file is empty")
		return
	}

	// In server mode, output should contain server metadata
	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Error("Failed to read output file:", err)
		return
	}
	// In server mode with colors, look for REMOTE or SERVER (without pipe as it may be colored)
	if !strings.Contains(string(content), "REMOTE") && !strings.Contains(string(content), "SERVER") {
		preview := string(content)
		if len(preview) > 500 {
			preview = preview[:500]
		}
		t.Errorf("Server mode output does not contain server metadata. First 500 chars:\n%s", preview)
		return
	}

	os.Remove(outFile)
}

func TestDGrep2(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrep2Serverless(t)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrep2WithServer(t)
	})
}

func testDGrep2Serverless(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep2.stdout.tmp"
	expectedOutFile := "dgrep2.txt.expected"

	_, err := runCommand(context.TODO(), t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		"--invert",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func testDGrep2WithServer(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep2.stdout.tmp"
	expectedOutFile := "dgrep2.txt.expected"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start dserver
	_, _, _, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "error",
		"--bindAddress", bindAddress,
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Error(err)
		return
	}

	// Give server time to start
	time.Sleep(500 * time.Millisecond)

	_, err = runCommand(ctx, t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		"--invert",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
		"--files", inFile)

	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func TestDGrepContext1(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrepContext1Serverless(t)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrepContext1WithServer(t)
	})
}

func testDGrepContext1Serverless(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepcontext1.stdout.tmp"
	expectedOutFile := "dgrepcontext1.txt.expected"

	_, err := runCommand(context.TODO(), t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		"--after", "3",
		"--before", "3", inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func testDGrepContext1WithServer(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepcontext1.stdout.tmp"
	expectedOutFile := "dgrepcontext1.txt.expected"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start dserver
	_, _, _, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "error",
		"--bindAddress", bindAddress,
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Error(err)
		return
	}

	// Give server time to start
	time.Sleep(500 * time.Millisecond)

	_, err = runCommand(ctx, t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		"--after", "3",
		"--before", "3",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
		"--files", inFile)

	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func TestDGrepContext2(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrepContext2Serverless(t)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrepContext2WithServer(t)
	})
}

func testDGrepContext2Serverless(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepcontext2.stdout.tmp"
	expectedOutFile := "dgrepcontext2.txt.expected"

	_, err := runCommand(context.TODO(), t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002",
		"--max", "3",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func testDGrepContext2WithServer(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepcontext2.stdout.tmp"
	expectedOutFile := "dgrepcontext2.txt.expected"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start dserver
	_, _, _, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "error",
		"--bindAddress", bindAddress,
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Error(err)
		return
	}

	// Give server time to start
	time.Sleep(500 * time.Millisecond)

	_, err = runCommand(ctx, t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002",
		"--max", "3",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
		"--files", inFile)

	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func TestDGrepStdin(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode only - stdin pipe doesn't make sense with server mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrepStdinServerless(t)
	})
}

func testDGrepStdinServerless(t *testing.T) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepstdin.stdout.tmp"
	expectedOutFile := "dgrep1.txt.expected" // Same expected output as TestDGrep1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create output file
	fd, err := os.Create(outFile)
	if err != nil {
		t.Error(err)
		return
	}
	defer fd.Close()

	// Use startCommand to pipe input via stdin
	stdoutCh, stderrCh, cmdErrCh, err := startCommand(ctx, t,
		inFile, "../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947")

	if err != nil {
		t.Error(err)
		return
	}

	// Collect output to file
	go func() {
		for line := range stdoutCh {
			fd.WriteString(line + "\n")
		}
	}()

	// Wait for command to complete
	for {
		select {
		case <-stderrCh:
			// Ignore stderr
		case cmdErr := <-cmdErrCh:
			if cmdErr != nil {
				t.Error("Command failed:", cmdErr)
			}
			// Give time for stdout goroutine to finish
			time.Sleep(100 * time.Millisecond)
			if err := compareFiles(t, outFile, expectedOutFile); err != nil {
				t.Error(err)
			}
			os.Remove(outFile)
			return
		case <-ctx.Done():
			t.Error("Test timed out")
			return
		}
	}
}