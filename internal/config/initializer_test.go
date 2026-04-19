package config

import (
	"os"
	"path/filepath"
	"testing"
)

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

func writeTestConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
}
