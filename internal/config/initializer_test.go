package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/source"
)

func TestSetupReturnsConfigDecodeError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "invalid.conf")
	writeTestConfig(t, configPath, `{not-json`)

	err := Setup(source.Client, &Args{ConfigFile: configPath}, nil)
	if err == nil {
		t.Fatal("Setup succeeded with invalid JSON")
	}
	for _, want := range []string{"load configuration", "parse config file", configPath} {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
			t.Fatalf("Setup error %q does not contain %q", err, want)
		}
	}
}

func TestSetupLogDirectoryReturnsHomeLookupError(t *testing.T) {
	original := userHomeDirectory
	userHomeDirectory = func() (string, error) { return "", errors.New("lookup failed") }
	t.Cleanup(func() { userHomeDirectory = original })

	in := initializer{Common: &CommonConfig{LogDir: "~/logs"}}
	err := setupLogDirectory(&in)
	if err == nil || !strings.Contains(err.Error(), "lookup failed") {
		t.Fatalf("setupLogDirectory error = %v, want wrapped lookup failure", err)
	}
}

func TestSetupRejectsInvalidSSHPort(t *testing.T) {
	for _, port := range []int{-1, 0, 65536} {
		t.Run(fmt.Sprintf("port_%d", port), func(t *testing.T) {
			err := Setup(source.Client, &Args{ConfigFile: "none", SSHPort: port}, nil)
			if err == nil || !strings.Contains(err.Error(), "SSH port must be between 1 and 65535") {
				t.Fatalf("Setup SSHPort %d error = %v, want port range error", port, err)
			}
		})
	}
}

func TestParseConfigLoadsDefaultXDGConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, ".config", "dtail", "dtail.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	writeTestConfig(t, configPath, `{"Common":{"LogLevel":"debug"}}`)

	in := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}

	if err := in.parseConfig(&Args{}); err != nil {
		t.Fatalf("parseConfig failed: %v", err)
	}
	if in.Common.LogLevel != "debug" {
		t.Fatalf("expected log level debug, got %q", in.Common.LogLevel)
	}
}

// TestParseConfigFirstWins verifies that when both candidate config files
// exist, the XDG path (~/.config/dtail/dtail.conf) takes precedence and the
// second file (~/.dtail.conf) is ignored entirely — no silent merging.
func TestParseConfigFirstWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	xdgPath := filepath.Join(home, ".config", "dtail", "dtail.conf")
	if err := os.MkdirAll(filepath.Dir(xdgPath), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	writeTestConfig(t, xdgPath, `{"Common":{"LogLevel":"warn"}}`)

	homePath := filepath.Join(home, ".dtail.conf")
	// The second file would override LogLevel to "error" if merging occurred.
	writeTestConfig(t, homePath, `{"Common":{"LogLevel":"error"}}`)

	in := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}

	if err := in.parseConfig(&Args{}); err != nil {
		t.Fatalf("parseConfig failed: %v", err)
	}
	// First-wins: the XDG config must have set the level; the home config
	// must have been skipped, so "error" must NOT appear.
	if in.Common.LogLevel != "warn" {
		t.Fatalf("expected log level warn (first file wins), got %q", in.Common.LogLevel)
	}
}

// TestParseConfigFallsBackToHomeConfig verifies that when only the legacy
// ~/.dtail.conf exists it is loaded as the effective configuration.
func TestParseConfigFallsBackToHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Only create the fallback file; the XDG directory does not exist.
	homePath := filepath.Join(home, ".dtail.conf")
	writeTestConfig(t, homePath, `{"Common":{"LogLevel":"error"}}`)

	in := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}

	if err := in.parseConfig(&Args{}); err != nil {
		t.Fatalf("parseConfig failed: %v", err)
	}
	if in.Common.LogLevel != "error" {
		t.Fatalf("expected log level error from fallback config, got %q", in.Common.LogLevel)
	}
}

// TestParseConfigNoConfigFile verifies that parseConfig succeeds without
// error when neither candidate config file is present.
func TestParseConfigNoConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	in := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}

	// No config files created; must return nil, not an error.
	if err := in.parseConfig(&Args{}); err != nil {
		t.Fatalf("expected no error when no config file exists, got: %v", err)
	}
}

// TestResolveSSHKeyPath verifies the three-level precedence used when
// resolving the effective SSH private key path.
func TestResolveSSHKeyPath(t *testing.T) {
	tests := []struct {
		name     string
		cli      string
		authKey  string
		legacy   string
		expected string
	}{
		{
			name:     "cli flag wins over both env vars",
			cli:      "/cli/key",
			authKey:  "/auth/key",
			legacy:   "/legacy/key",
			expected: "/cli/key",
		},
		{
			name:     "DTAIL_AUTH_KEY_PATH wins over legacy when cli is empty",
			cli:      "",
			authKey:  "/auth/key",
			legacy:   "/legacy/key",
			expected: "/auth/key",
		},
		{
			name:     "DTAIL_SSH_PRIVATE_KEYFILE_PATH used when auth key env is also empty",
			cli:      "",
			authKey:  "",
			legacy:   "/legacy/key",
			expected: "/legacy/key",
		},
		{
			name:     "all empty returns empty string",
			cli:      "",
			authKey:  "",
			legacy:   "",
			expected: "",
		},
		{
			name:     "cli wins when only cli is set",
			cli:      "/cli/key",
			authKey:  "",
			legacy:   "",
			expected: "/cli/key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveSSHKeyPath(tc.cli, tc.authKey, tc.legacy)
			if got != tc.expected {
				t.Fatalf("resolveSSHKeyPath(%q, %q, %q) = %q; want %q",
					tc.cli, tc.authKey, tc.legacy, got, tc.expected)
			}
		})
	}
}

func TestIntegrationSSHPrivateKeyPath(t *testing.T) {
	t.Run("Environment", func(t *testing.T) {
		t.Setenv("DTAIL_AUTH_KEY_PATH", "/tmp/integration/id_rsa")
		if got := IntegrationSSHPrivateKeyPath(); got != "/tmp/integration/id_rsa" {
			t.Fatalf("IntegrationSSHPrivateKeyPath() = %q, want environment path", got)
		}
	})

	t.Run("LegacyFallback", func(t *testing.T) {
		t.Setenv("DTAIL_AUTH_KEY_PATH", "")
		if got := IntegrationSSHPrivateKeyPath(); got != "./id_rsa" {
			t.Fatalf("IntegrationSSHPrivateKeyPath() = %q, want ./id_rsa", got)
		}
	})
}

// TestProcessEnvVarsAuthKeyPathTakesPrecedence is a negative/regression test
// that confirms the bug described in task k6 is fixed: when both
// DTAIL_AUTH_KEY_PATH and DTAIL_SSH_PRIVATE_KEYFILE_PATH are set,
// DTAIL_AUTH_KEY_PATH must win.
func TestProcessEnvVarsAuthKeyPathTakesPrecedence(t *testing.T) {
	t.Setenv("DTAIL_AUTH_KEY_PATH", "/env/auth/key")
	t.Setenv("DTAIL_SSH_PRIVATE_KEYFILE_PATH", "/env/legacy/key")

	in := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}
	args := &Args{}
	in.processEnvVars(args)

	if args.SSHPrivateKeyFilePath != "/env/auth/key" {
		t.Fatalf("expected DTAIL_AUTH_KEY_PATH to win, got %q", args.SSHPrivateKeyFilePath)
	}
}

// TestProcessEnvVarsLegacyFallback verifies that DTAIL_SSH_PRIVATE_KEYFILE_PATH
// is still applied when DTAIL_AUTH_KEY_PATH is not set.
func TestProcessEnvVarsLegacyFallback(t *testing.T) {
	t.Setenv("DTAIL_AUTH_KEY_PATH", "")
	t.Setenv("DTAIL_SSH_PRIVATE_KEYFILE_PATH", "/env/legacy/key")

	in := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}
	args := &Args{}
	in.processEnvVars(args)

	if args.SSHPrivateKeyFilePath != "/env/legacy/key" {
		t.Fatalf("expected legacy env var to be used, got %q", args.SSHPrivateKeyFilePath)
	}
}

// TestProcessEnvVarsCLIFlagNotOverridden verifies that an explicit CLI flag
// value is not overridden by either environment variable.
func TestProcessEnvVarsCLIFlagNotOverridden(t *testing.T) {
	t.Setenv("DTAIL_AUTH_KEY_PATH", "/env/auth/key")
	t.Setenv("DTAIL_SSH_PRIVATE_KEYFILE_PATH", "/env/legacy/key")

	in := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}
	args := &Args{SSHPrivateKeyFilePath: "/cli/explicit/key"}
	in.processEnvVars(args)

	if args.SSHPrivateKeyFilePath != "/cli/explicit/key" {
		t.Fatalf("expected CLI flag to be preserved, got %q", args.SSHPrivateKeyFilePath)
	}
}

// TestDefaultAuthKeyPathNoLiteralTilde is a regression test for the bug
// described in task l6: when neither os.UserHomeDir() nor the HOME environment
// variable can be resolved, defaultAuthKeyPath must return "" rather than the
// literal string "~/.ssh/id_rsa" which the SSH library cannot expand.
func TestDefaultAuthKeyPathNoLiteralTilde(t *testing.T) {
	// Unset HOME so that os.UserHomeDir() fails and the fallback env var is
	// also empty. t.Setenv restores the original value after the test.
	t.Setenv("HOME", "")

	got := defaultAuthKeyPath()
	if got == "~/.ssh/id_rsa" {
		t.Fatal("defaultAuthKeyPath returned literal '~/.ssh/id_rsa' when HOME is empty; expected \"\"")
	}
	if got != "" {
		t.Fatalf("defaultAuthKeyPath returned %q when HOME is empty; expected \"\"", got)
	}
}

// TestDefaultAuthKeyPathWithHome verifies that when HOME is set, the returned
// path is an absolute path constructed with filepath.Join (no literal '~').
func TestDefaultAuthKeyPathWithHome(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")

	got := defaultAuthKeyPath()
	want := "/home/testuser/.ssh/id_rsa"
	if got != want {
		t.Fatalf("defaultAuthKeyPath() = %q; want %q", got, want)
	}
}

func writeTestConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
}
