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

func TestDTailWithServer(t *testing.T) {
	testLogger := NewTestLogger("TestDTailWithServer")
	defer testLogger.WriteLogFile()
	cleanupTmpFiles(t)
	
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}
	followFile := "dtail.follow.tmp"
	port := getUniquePortNumber()
	bindAddress := "localhost"
	greetings := []string{"World!", "Sol-System!", "Milky-Way!", "Universe!", "Multiverse!"}

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithTestLogger(ctx, testLogger)
	defer cancel()

	go func() {
		select {
		case <-time.After(time.Minute):
			t.Error("Max time for this test exceeded!")
			cancel()
		case <-ctx.Done():
			return
		}
	}()

	serverCh, _, _, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "info",
		"--bindAddress", bindAddress,
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Error(err)
		return
	}

	// MAYBETODO: In testmode, never read a config file (use none for all commands)
	clientCh, _, _, err := startCommand(ctx, t,
		"", "../dtail",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "info",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--files", followFile,
		"--grep", "Hello",
		"--trustAllHosts",
		"--noColor",
	)
	if err != nil {
		t.Error(err)
		return
	}
	// Write greetings to followFile
	fd, err := os.Create(followFile)
	if err != nil {
		t.Error(err)
	}
	defer fd.Close()

	go func() {
		var circular int
		for {
			select {
			case <-time.After(time.Second):
				_, _ = fd.WriteString(time.Now().String())
				_, _ = fd.WriteString(fmt.Sprintf(" - Hello %s\n", greetings[circular]))
				circular = (circular + 1) % len(greetings)
			case <-ctx.Done():
				return
			}
		}
	}()

	var greetingsRecv []string

	for len(greetingsRecv) < len(greetings) {
		select {
		case line := <-serverCh:
			t.Log("server:", line)
		case line := <-clientCh:
			t.Log("client:", line)
			if strings.Contains(line, "Hello ") {
				s := strings.Split(line, " ")
				greeting := s[len(s)-1]
				greetingsRecv = append(greetingsRecv, greeting)
				t.Log("Received greeting", greeting, len(greetingsRecv))
			}
		case <-ctx.Done():
			t.Log("Done reading client and server pipes")
			break
		}
	}

	// We expect to have received the greetings in the same order they were sent.`
	offset := -1
	for i, g := range greetings {
		if g == greetingsRecv[0] {
			offset = i
			break
		}
	}
	if offset == -1 {
		t.Error("Could not find first offset of greetings received")
		return
	}

	for i, g := range greetingsRecv {
		index := (i + offset) % len(greetings)
		if greetings[index] != g {
			t.Errorf("Expected '%s' but got '%s' at '%v' vs '%v'\n",
				g, greetings[index], greetings, greetingsRecv)
			return
		}
	}

	// File cleanup handled by cleanupTmpFiles
}

func TestDTailColorTable(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	cleanupTmpFiles(t)
	testLogger := NewTestLogger("TestDTailColorTable")
	defer testLogger.WriteLogFile()

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		testDTailColorTableServerless(t, testLogger)
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		testDTailColorTableWithServer(t, testLogger)
	})
}

func testDTailColorTableServerless(t *testing.T, logger *TestLogger) {
	ctx := WithTestLogger(context.Background(), logger)
	
	outFile := "dtailcolortable.stdout.tmp"
	expectedOutFile := "dtailcolortable.expected"

	_, err := runCommand(ctx, t, outFile, "../dtail", "--colorTable")
	if err != nil {
		t.Error(err)
		return
	}
	if err := compareFilesWithContext(ctx, t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}
}

func testDTailColorTableWithServer(t *testing.T, logger *TestLogger) {
	outFile := "dtailcolortable.stdout.tmp"
	expectedOutFile := "dtailcolortable.expected"
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

	_, err = runCommand(ctx, t, outFile, "../dtail", 
		"--colorTable",
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts")
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
