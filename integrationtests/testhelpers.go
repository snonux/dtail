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

// skipIfNotIntegrationTest skips the test if integration tests are not enabled
func skipIfNotIntegrationTest(t *testing.T) {
	t.Helper()
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Skip("Skipping integration test")
	}
}

// ServerConfig contains configuration for starting a test server
type ServerConfig struct {
	Port        int
	BindAddress string
	LogLevel    string
	ExtraArgs   []string
}

// DefaultServerConfig returns a default server configuration
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Port:        getUniquePortNumber(),
		BindAddress: "localhost",
		LogLevel:    "error",
	}
}

// startTestServer starts a dserver with the given configuration
func startTestServer(t *testing.T, ctx context.Context, cfg *ServerConfig) error {
	t.Helper()
	if cfg == nil {
		cfg = DefaultServerConfig()
	}

	args := []string{
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", cfg.LogLevel,
		"--bindAddress", cfg.BindAddress,
		"--port", fmt.Sprintf("%d", cfg.Port),
	}
	args = append(args, cfg.ExtraArgs...)

	_, _, _, err := startCommand(ctx, t, "", "../dserver", args...)
	if err != nil {
		return err
	}

	// Give server time to start
	time.Sleep(500 * time.Millisecond)
	return nil
}

// createTestContext creates a context with cancel that will be cleaned up automatically
func createTestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
	})
	return ctx, cancel
}

// cleanupFiles registers files to be removed during test cleanup
func cleanupFiles(t *testing.T, files ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, file := range files {
			os.Remove(file)
		}
	})
}

// TestServer manages a test server lifecycle
type TestServer struct {
	t           *testing.T
	ctx         context.Context
	cancel      context.CancelFunc
	port        int
	bindAddress string
}

// NewTestServer creates a new test server manager
func NewTestServer(t *testing.T) *TestServer {
	t.Helper()
	ctx, cancel := createTestContext(t)
	return &TestServer{
		t:           t,
		ctx:         ctx,
		cancel:      cancel,
		port:        getUniquePortNumber(),
		bindAddress: "localhost",
	}
}

// Start starts the test server with the given log level
func (ts *TestServer) Start(logLevel string) error {
	return startTestServer(ts.t, ts.ctx, &ServerConfig{
		Port:        ts.port,
		BindAddress: ts.bindAddress,
		LogLevel:    logLevel,
	})
}

// StartWithConfig starts the test server with custom configuration
func (ts *TestServer) StartWithConfig(cfg *ServerConfig) error {
	if cfg == nil {
		cfg = &ServerConfig{}
	}
	cfg.Port = ts.port
	cfg.BindAddress = ts.bindAddress
	return startTestServer(ts.t, ts.ctx, cfg)
}

// Address returns the server address in host:port format
func (ts *TestServer) Address() string {
	return fmt.Sprintf("%s:%d", ts.bindAddress, ts.port)
}

// Stop stops the test server
func (ts *TestServer) Stop() {
	ts.cancel()
}

// CommandArgs contains common command-line arguments
type CommandArgs struct {
	Config        string
	LogLevel      string
	Logger        string
	Plain         bool
	NoColor       bool
	Servers       []string
	TrustAllHosts bool
	Files         []string
	ExtraArgs     []string
}

// NewCommandArgs creates command args with sensible defaults
func NewCommandArgs() *CommandArgs {
	return &CommandArgs{
		Config: "none",
	}
}

// ToSlice converts CommandArgs to a string slice for command execution
func (c *CommandArgs) ToSlice() []string {
	args := []string{"--cfg", c.Config}
	
	if c.LogLevel != "" {
		args = append(args, "--logLevel", c.LogLevel)
	}
	if c.Logger != "" {
		args = append(args, "--logger", c.Logger)
	}
	if c.Plain {
		args = append(args, "--plain")
	}
	if c.NoColor {
		args = append(args, "--noColor")
	}
	if len(c.Servers) > 0 {
		args = append(args, "--servers", strings.Join(c.Servers, ","))
	}
	if c.TrustAllHosts {
		args = append(args, "--trustAllHosts")
	}
	if len(c.Files) > 0 {
		args = append(args, "--files", strings.Join(c.Files, ","))
	}
	
	return append(args, c.ExtraArgs...)
}

// DualModeTest represents a test that runs in both serverless and server modes
type DualModeTest struct {
	Name           string
	ServerlessTest func(t *testing.T)
	ServerTest     func(t *testing.T)
}

// runDualModeTest runs a test in both serverless and server modes
func runDualModeTest(t *testing.T, test DualModeTest) {
	skipIfNotIntegrationTest(t)

	if test.ServerlessTest != nil {
		t.Run("Serverless", test.ServerlessTest)
	}

	if test.ServerTest != nil {
		t.Run("ServerMode", test.ServerTest)
	}
}

// verifyFileExists checks if a file exists and is not empty
func verifyFileExists(t *testing.T, filename string) error {
	t.Helper()
	
	info, err := os.Stat(filename)
	if err != nil {
		return fmt.Errorf("file %s not created: %w", filename, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("file %s is empty", filename)
	}
	
	return nil
}

// verifyColoredOutput verifies that output contains ANSI color codes and optionally server metadata
func verifyColoredOutput(t *testing.T, outFile string, expectServerMetadata bool) error {
	t.Helper()

	if err := verifyFileExists(t, outFile); err != nil {
		return err
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		return fmt.Errorf("failed to read output file: %w", err)
	}

	// Check for ANSI color codes
	if !strings.Contains(string(content), "\033[") {
		return fmt.Errorf("output does not contain ANSI color codes")
	}

	// Check for server metadata if expected
	if expectServerMetadata {
		if !strings.Contains(string(content), "REMOTE") && !strings.Contains(string(content), "SERVER") && !strings.Contains(string(content), "CLIENT") {
			preview := string(content)
			if len(preview) > 500 {
				preview = preview[:500]
			}
			return fmt.Errorf("server mode output does not contain server metadata. First 500 chars:\n%s", preview)
		}
	}

	return nil
}

// runCommandAndVerify runs a command and verifies the output against an expected file
func runCommandAndVerify(t *testing.T, ctx context.Context, outFile, expectedFile, cmd string, args ...string) error {
	t.Helper()

	_, err := runCommand(ctx, t, outFile, cmd, args...)
	if err != nil {
		return err
	}

	if err := compareFiles(t, outFile, expectedFile); err != nil {
		return err
	}

	return nil
}

// runCommandAndVerifyContents runs a command and verifies the output contents (ignoring order)
func runCommandAndVerifyContents(t *testing.T, ctx context.Context, outFile, expectedFile, cmd string, args ...string) error {
	t.Helper()

	_, err := runCommand(ctx, t, outFile, cmd, args...)
	if err != nil {
		return err
	}

	if err := compareFilesContents(t, outFile, expectedFile); err != nil {
		return err
	}

	return nil
}

// TestFileSet represents a set of test files
type TestFileSet struct {
	InputFile    string
	OutputFile   string
	ExpectedFile string
	ExtraFiles   []string // Additional files to clean up (e.g., .query files)
}

// Cleanup registers all files in the set for cleanup
func (tfs *TestFileSet) Cleanup(t *testing.T) {
	t.Helper()
	files := []string{tfs.OutputFile}
	files = append(files, tfs.ExtraFiles...)
	cleanupFiles(t, files...)
}

// StandardTestPaths returns common test file paths
type StandardTestPaths struct {
	MaprTestData string
	DCat1Files   []string
	DCat2File    string
	DCat3File    string
	ColorFile    string
}

// GetStandardTestPaths returns commonly used test file paths
func GetStandardTestPaths() *StandardTestPaths {
	return &StandardTestPaths{
		MaprTestData: "mapr_testdata.log",
		DCat1Files:   []string{"dcat1a.txt", "dcat1b.txt", "dcat1c.txt", "dcat1d.txt"},
		DCat2File:    "dcat2.txt",
		DCat3File:    "dcat3.txt",
		ColorFile:    "dcatcolors.txt",
	}
}