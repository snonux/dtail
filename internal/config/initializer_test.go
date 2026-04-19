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

func writeTestConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
}
