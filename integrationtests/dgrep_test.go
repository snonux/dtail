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

	cleanupTmpFiles(t)
	testLogger := NewTestLogger("TestDGrep1")
	defer testLogger.WriteLogFile()

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrep1Serverless(t, testLogger)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrep1WithServer(t, testLogger)
	})
}

func testDGrep1Serverless(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep.stdout.tmp"
	expectedOutFile := "dgrep1.txt.expected"
	ctx := WithTestLogger(context.Background(), logger)

	_, err := runCommand(ctx, t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}

func testDGrep1WithServer(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep.stdout.tmp"
	expectedOutFile := "dgrep1.txt.expected"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithTestLogger(ctx, logger)
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

	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}

func TestDGrep1Colors(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	cleanupTmpFiles(t)
	testLogger := NewTestLogger("TestDGrep1Colors")
	defer testLogger.WriteLogFile()

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrep1ColorsServerless(t, testLogger)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrep1ColorsWithServer(t, testLogger)
	})
}

func testDGrep1ColorsServerless(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep1colors.stdout.tmp"
	ctx := WithTestLogger(context.Background(), logger)

	// Run without --plain to get colored output
	_, err := runCommand(ctx, t, outFile,
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

	// Log verification
	logger.LogFileComparison(outFile, "ANSI color codes", "contains check")
}

func testDGrep1ColorsWithServer(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep1colors.stdout.tmp"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithTestLogger(ctx, logger)
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
	if !strings.Contains(string(content), "REMOTE") && !strings.Contains(string(content), "SERVER") {
		preview := string(content)
		if len(preview) > 500 {
			preview = preview[:500]
		}
		t.Errorf("Server mode output does not contain server metadata. First 500 chars:\n%s", preview)
		return
	}

	// Log verification
	logger.LogFileComparison(outFile, "server metadata (REMOTE/SERVER)", "contains check")
}

func TestDGrep2(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	cleanupTmpFiles(t)
	testLogger := NewTestLogger("TestDGrep2")
	defer testLogger.WriteLogFile()

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrep2Serverless(t, testLogger)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrep2WithServer(t, testLogger)
	})
}

func testDGrep2Serverless(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep.stdout.tmp"
	expectedOutFile := "dgrep2.txt.expected"
	ctx := WithTestLogger(context.Background(), logger)

	_, err := runCommand(ctx, t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-07194[789]",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}

func testDGrep2WithServer(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrep.stdout.tmp"
	expectedOutFile := "dgrep2.txt.expected"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithTestLogger(ctx, logger)
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
		"--grep", "1002-07194[789]",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
		"--files", inFile)

	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}

func TestDGrepContext1(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	cleanupTmpFiles(t)
	testLogger := NewTestLogger("TestDGrepContext1")
	defer testLogger.WriteLogFile()

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrepContext1Serverless(t, testLogger)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrepContext1WithServer(t, testLogger)
	})
}

func testDGrepContext1Serverless(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepcontext.stdout.tmp"
	expectedOutFile := "dgrepcontext1.txt.expected"
	ctx := WithTestLogger(context.Background(), logger)

	_, err := runCommand(ctx, t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		"--before", "2",
		"--after", "3",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}

func testDGrepContext1WithServer(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepcontext.stdout.tmp"
	expectedOutFile := "dgrepcontext1.txt.expected"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithTestLogger(ctx, logger)
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
		"--before", "2",
		"--after", "3",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
		"--files", inFile)

	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}

func TestDGrepContext2(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	cleanupTmpFiles(t)
	testLogger := NewTestLogger("TestDGrepContext2")
	defer testLogger.WriteLogFile()

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrepContext2Serverless(t, testLogger)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDGrepContext2WithServer(t, testLogger)
	})
}

func testDGrepContext2Serverless(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepcontext.stdout.tmp"
	expectedOutFile := "dgrepcontext2.txt.expected"
	ctx := WithTestLogger(context.Background(), logger)

	_, err := runCommand(ctx, t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		"--before", "2",
		"--after", "3",
		"--invert",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}

func testDGrepContext2WithServer(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepcontext.stdout.tmp"
	expectedOutFile := "dgrepcontext2.txt.expected"
	port := getUniquePortNumber()
	bindAddress := "localhost"

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithTestLogger(ctx, logger)
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
		"--before", "2",
		"--after", "3",
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

	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}

func TestDGrepPipeToStdin(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	cleanupTmpFiles(t)
	testLogger := NewTestLogger("TestDGrepPipeToStdin")
	defer testLogger.WriteLogFile()

	// Only test in serverless mode (stdin piping doesn't work with server mode)
	t.Run("Serverless", func(t *testing.T) {
		testDGrepStdinServerless(t, testLogger)
	})
}

func testDGrepStdinServerless(t *testing.T, logger *TestLogger) {
	inFile := "mapr_testdata.log"
	outFile := "dgrepstdin.stdout.tmp" 
	expectedOutFile := "dgrep1.txt.expected"
	ctx := WithTestLogger(context.Background(), logger)

	// Use startCommand with stdin piping
	stdoutCh, stderrCh, cmdErrCh, err := startCommand(ctx, t,
		inFile, // This will be piped to stdin
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947")
	
	if err != nil {
		t.Error(err)
		return
	}

	// Collect output
	fd, err := os.Create(outFile)
	if err != nil {
		t.Error(err)
		return
	}
	defer fd.Close()

	// Read from stdout channel
	go func() {
		for line := range stdoutCh {
			fmt.Fprintln(fd, line)
		}
	}()

	// Drain stderr channel
	go func() {
		for line := range stderrCh {
			t.Log("stderr:", line)
		}
	}()

	// Wait for command to complete
	select {
	case <-ctx.Done():
		t.Error("Context cancelled")
		return
	case cmdErr := <-cmdErrCh:
		if cmdErr != nil {
			t.Error("Command failed:", cmdErr)
			return
		}
	case <-time.After(5 * time.Second):
		t.Error("Command timed out")
		return
	}

	// Give time for output to be written
	time.Sleep(100 * time.Millisecond)

	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}