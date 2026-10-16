package integrationtests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

// The UTF-8 encodings of '€' (E2 82 AC), 'Ь' (D0 AC), '¬' (C2 AC) and '丬'
// (E4 B8 AC) end in 0xAC, the byte that delimits protocol messages. Before
// escaped framing (protocol.CapabilityEscapedFramingV1), server mode cut each
// such line at that byte and lost it, while serverless mode printed it whole.
const (
	utf8FramingCatFile         = "dcat_utf8.txt"
	utf8FramingMapInFile       = "dmap_utf8.csv.in"
	utf8FramingMapExpectedFile = "dmap_utf8.csv.expected"
	utf8FramingMapQuery        = "select label,count($line),last(value) group by label logformat csv"
)

// TestUTF8Framing checks that dcat in server mode prints a file with such
// characters byte for byte as serverless dcat and the file itself do, and
// that dmap group keys holding them aggregate correctly in both modes.
func TestUTF8Framing(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}
	cleanupTmpFiles(t)
	testLogger := NewTestLogger("TestUTF8Framing")
	defer writeLogFileIgnoringError(testLogger)

	t.Run("DCatServerMatchesServerless", func(t *testing.T) {
		testUTF8FramingDCat(t, testLogger)
	})
	t.Run("DMapServerless", func(t *testing.T) {
		testUTF8FramingDMap(t, testLogger, false)
	})
	t.Run("DMapServer", func(t *testing.T) {
		testUTF8FramingDMap(t, testLogger, true)
	})
}

func testUTF8FramingDCat(t *testing.T, logger *TestLogger) {
	serverlessOut := "dcat_utf8_serverless.tmp"
	serverOut := "dcat_utf8_server.tmp"
	ctx := WithTestLogger(context.Background(), logger)

	if _, err := runCommand(ctx, t, serverlessOut,
		"../dcat", "--plain", "--cfg", "none", utf8FramingCatFile); err != nil {
		t.Fatal(err)
	}
	if err := compareFilesWithContext(ctx, t, serverlessOut, utf8FramingCatFile); err != nil {
		t.Fatalf("serverless dcat changed the file: %v", err)
	}

	server := NewTestServer(t)
	if err := server.Start("error"); err != nil {
		t.Fatal(err)
	}
	err := runCommandUntilValid(server.ctx, t, 5, 200*time.Millisecond, serverOut, "../dcat",
		func() error { return compareFilesWithContext(ctx, t, serverOut, serverlessOut) },
		"--plain", "--cfg", "none",
		"--servers", server.Address(),
		"--files", utf8FramingCatFile,
		"--trustAllHosts", "--noColor")
	if err != nil {
		t.Fatalf("server-mode dcat output differs from serverless dcat: %v", err)
	}
}

func testUTF8FramingDMap(t *testing.T, logger *TestLogger, withServer bool) {
	mode := "serverless"
	if withServer {
		mode = "server"
	}
	csvFile := fmt.Sprintf("dmap_utf8_%s.csv.tmp", mode)
	outFile := fmt.Sprintf("dmap_utf8_%s.stdout.tmp", mode)
	cleanupFiles(t, csvFile, csvFile+".query", outFile)
	query := fmt.Sprintf("%s outfile %s", utf8FramingMapQuery, csvFile)

	ctxTimeout, cancel := createTestContextWithTimeout(t)
	defer cancel()
	ctx := WithTestLogger(ctxTimeout, logger)
	args := []string{"--cfg", "none", "--query", query, utf8FramingMapInFile}
	if withServer {
		server := NewTestServer(t)
		if err := server.Start("error"); err != nil {
			t.Fatal(err)
		}
		args = []string{"--cfg", "none", "--query", query,
			"--servers", server.Address(), "--files", utf8FramingMapInFile,
			"--trustAllHosts", "--noColor"}
	}
	if _, err := runCommand(ctx, t, outFile, "../dmap", args...); err != nil {
		t.Fatal(err)
	}
	if err := compareFilesContentsWithContext(ctx, t, csvFile, utf8FramingMapExpectedFile); err != nil {
		t.Fatalf("%s dmap result: %v", mode, err)
	}
}
