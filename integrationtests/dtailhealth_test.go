package integrationtests

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

func TestDTailHealth1(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDTailHealth1Serverless(t)
	})

	// Test in server mode - this test checks when no servers are specified
	// so server mode behavior should be the same
	t.Run("ServerMode", func(t *testing.T) {
		testDTailHealth1WithServer(t)
	})
}

func testDTailHealth1Serverless(t *testing.T) {
	outFile := "dtailhealth1.stdout.tmp"
	expectedOutFile := "dtailhealth1.expected"

	t.Log("Serverless check, is supposed to exit with warning state.")
	exitCode, err := runCommand(context.TODO(), t, outFile, "../dtailhealth")
	if exitCode != 1 {
		t.Errorf("Expected exit code '1' but got '%d': %v", exitCode, err)
		return
	}

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
	os.Remove(outFile)
}

func testDTailHealth1WithServer(t *testing.T) {
	outFile := "dtailhealth1.stdout.tmp"
	expectedOutFile := "dtailhealth1.expected"
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

	t.Log("Server mode check without --server flag, is supposed to exit with warning state.")
	// Run dtailhealth without specifying --server flag
	exitCode, err := runCommand(ctx, t, outFile, "../dtailhealth")
	if exitCode != 1 {
		t.Errorf("Expected exit code '1' but got '%d': %v", exitCode, err)
		return
	}

	cancel()

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
	os.Remove(outFile)
}

func TestDTailHealth2(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDTailHealth2Serverless(t)
	})

	// Test in server mode - testing unreachable server
	t.Run("ServerMode", func(t *testing.T) {
		testDTailHealth2WithServer(t)
	})
}

func testDTailHealth2Serverless(t *testing.T) {
	outFile := "dtailhealth2.stdout.tmp"
	expectedOutFile := "dtailhealth2.expected"

	t.Log("Negative test, is supposed to exit with a critical state.")
	exitCode, err := runCommand(context.TODO(), t, outFile,
		"../dtailhealth", "--server", "example:1")

	if exitCode != 2 {
		t.Error(fmt.Sprintf("Expected exit code '2' but got '%d': %v", exitCode, err))
		return
	}

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func testDTailHealth2WithServer(t *testing.T) {
	outFile := "dtailhealth2.stdout.tmp"
	expectedOutFile := "dtailhealth2.expected"
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

	t.Log("Server mode negative test, checking unreachable server, is supposed to exit with a critical state.")
	// Check an unreachable server (not the one we started)
	exitCode, err := runCommand(ctx, t, outFile,
		"../dtailhealth", "--server", "example:1")

	if exitCode != 2 {
		t.Error(fmt.Sprintf("Expected exit code '2' but got '%d': %v", exitCode, err))
		return
	}

	cancel()

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func TestDTailHealthCheck3(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// This test only makes sense with a server
	t.Run("ServerMode", func(t *testing.T) {
		testDTailHealthCheck3WithServer(t)
	})
}

func testDTailHealthCheck3WithServer(t *testing.T) {
	outFile := "dtailhealth3.stdout.tmp"
	port := getUniquePortNumber()
	bindAddress := "localhost"
	expectedOut := fmt.Sprintf("OK: All fine at %s:%d :-)", bindAddress, port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, _, _, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "trace",
		"--bindAddress", bindAddress,
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Error(err)
		return
	}

	_, err = runCommandRetry(ctx, t, 10, outFile,
		"../dtailhealth", "--server", fmt.Sprintf("%s:%d", bindAddress, port))
	if err != nil {
		t.Error(err)
		return
	}

	if err := fileContainsStr(t, outFile, expectedOut); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}
