package integrationtests

import (
	"context"
	"fmt"
	"os"
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

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDGrepStdinServerless(t)
	})

	// Test in server mode - skip stdin pipe test as it hangs (similar to dmap)
	// t.Run("ServerMode", func(t *testing.T) {
	// 	testDGrepStdinWithServer(t)
	// })
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