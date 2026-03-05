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

func TestParseConfigLoadsDefaultConfigsInOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	xdgPath := filepath.Join(home, ".config", "dtail", "dtail.conf")
	if err := os.MkdirAll(filepath.Dir(xdgPath), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	writeTestConfig(t, xdgPath, `{"Common":{"Logger":"file","LogLevel":"warn"}}`)

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
		t.Fatalf("expected final log level error, got %q", in.Common.LogLevel)
	}
	if in.Common.Logger != "file" {
		t.Fatalf("expected logger file from first config, got %q", in.Common.Logger)
	}
}

func writeTestConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
}
