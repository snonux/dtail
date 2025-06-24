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

func TestDCat1(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		inFiles := []string{"dcat1a.txt", "dcat1b.txt", "dcat1c.txt", "dcat1d.txt"}
		for _, inFile := range inFiles {
			if err := testDCat1Serverless(t, inFile); err != nil {
				t.Error(err)
				return
			}
		}
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		inFiles := []string{"dcat1a.txt", "dcat1b.txt", "dcat1c.txt", "dcat1d.txt"}
		for _, inFile := range inFiles {
			if err := testDCat1WithServer(t, inFile); err != nil {
				t.Error(err)
				return
			}
		}
	})
}

func testDCat1Serverless(t *testing.T, inFile string) error {
	outFile := "dcat1.out"

	_, err := runCommand(context.TODO(), t, outFile,
		"../dcat", "--plain", "--cfg", "none", inFile)
	if err != nil {
		return err
	}
	if err := compareFiles(t, outFile, inFile); err != nil {
		return err
	}

	os.Remove(outFile)
	return nil
}

func testDCat1WithServer(t *testing.T, inFile string) error {
	outFile := "dcat1.out"
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
		return err
	}

	// Give server time to start
	time.Sleep(500 * time.Millisecond)

	// Run dcat against the server
	_, err = runCommand(ctx, t, outFile,
		"../dcat", "--plain", "--cfg", "none",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--files", inFile,
		"--trustAllHosts",
		"--noColor")
	if err != nil {
		return err
	}

	cancel()

	if err := compareFiles(t, outFile, inFile); err != nil {
		return err
	}

	os.Remove(outFile)
	return nil
}

func TestDCat1Colors(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDCat1ColorsServerless(t)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDCat1ColorsWithServer(t)
	})
}

func testDCat1ColorsServerless(t *testing.T) {
	inFile := "dcat1a.txt"
	outFile := "dcat1colors.out"

	// Run without --plain to get colored output
	_, err := runCommand(context.TODO(), t, outFile,
		"../dcat", "--cfg", "none", inFile)
	if err != nil {
		t.Error(err)
		return
	}

	// Just verify it ran successfully and produced output
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

func testDCat1ColorsWithServer(t *testing.T) {
	inFile := "dcat1a.txt"
	outFile := "dcat1colors.out"
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
		"../dcat", "--cfg", "none",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--files", inFile,
		"--trustAllHosts")
	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	// Just verify it ran successfully and produced output
	info, err := os.Stat(outFile)
	if err != nil {
		t.Error("Output file not created:", err)
		return
	}
	if info.Size() == 0 {
		t.Error("Output file is empty")
		return
	}

	// In server mode, output should contain server metadata unless --plain is used
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

func TestDCat2(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDCat2Serverless(t)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDCat2WithServer(t)
	})
}

func testDCat2Serverless(t *testing.T) {
	inFile := "dcat2.txt"
	expectedFile := "dcat2.txt.expected"
	outFile := "dcat2.out"

	args := []string{"--plain", "--logLevel", "error", "--cfg", "none"}

	// Cat file 100 times in one session.
	for i := 0; i < 100; i++ {
		args = append(args, inFile)
	}

	_, err := runCommand(context.TODO(), t, outFile, "../dcat", args...)
	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesContents(t, outFile, expectedFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func testDCat2WithServer(t *testing.T) {
	inFile := "dcat2.txt"
	expectedFile := "dcat2.txt.expected"
	outFile := "dcat2.out"
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

	// Cat file 100 times in one session.
	var files []string
	for i := 0; i < 100; i++ {
		files = append(files, inFile)
	}
	
	args := []string{"--plain", "--logLevel", "error", "--cfg", "none",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts", "--noColor", "--files", strings.Join(files, ",")}

	_, err = runCommand(ctx, t, outFile, "../dcat", args...)
	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	if err := compareFilesContents(t, outFile, expectedFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func TestDCat3(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDCat3Serverless(t)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDCat3WithServer(t)
	})
}

func testDCat3Serverless(t *testing.T) {
	inFile := "dcat3.txt"
	expectedFile := "dcat3.txt.expected"
	outFile := "dcat3.out"

	args := []string{"--plain", "--logLevel", "error", "--cfg", "none", inFile}

	// Notice, with DTAIL_INTEGRATION_TEST_RUN_MODE the DTail max line length is set
	// to 1024!
	_, err := runCommand(context.TODO(), t, outFile, "../dcat", args...)
	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesContents(t, outFile, expectedFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func testDCat3WithServer(t *testing.T) {
	inFile := "dcat3.txt"
	expectedFile := "dcat3.txt.expected"
	outFile := "dcat3.out"
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

	args := []string{"--plain", "--logLevel", "error", "--cfg", "none",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--files", inFile,
		"--trustAllHosts",
		"--noColor"}

	// Notice, with DTAIL_INTEGRATION_TEST_RUN_MODE the DTail max line length is set
	// to 1024!
	_, err = runCommand(ctx, t, outFile, "../dcat", args...)
	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	if err := compareFilesContents(t, outFile, expectedFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func TestDCatColors(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		return
	}

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDCatColorsServerless(t)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDCatColorsWithServer(t)
	})
}

func testDCatColorsServerless(t *testing.T) {
	inFile := "dcatcolors.txt"
	outFile := "dcatcolors.out"
	expectedFile := "dcatcolors.expected"

	_, err := runCommand(context.TODO(), t, outFile,
		"../dcat", "--logLevel", "error", "--cfg", "none", inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFiles(t, outFile, expectedFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func testDCatColorsWithServer(t *testing.T) {
	inFile := "dcatcolors.txt"
	outFile := "dcatcolors.out"
	expectedFile := "dcatcolors.server.expected"
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
		"../dcat", "--logLevel", "error", "--cfg", "none",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--files", inFile,
		"--trustAllHosts",
		"--noColor")

	if err != nil {
		t.Error(err)
		return
	}

	cancel()

	if err := compareFiles(t, outFile, expectedFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}
