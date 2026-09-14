package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/config"
)

func TestNewReturnsMalformedHostKeyError(t *testing.T) {
	t.Setenv("DTAIL_INTEGRATION_TEST_RUN_MODE", "")
	hostKeyFile := filepath.Join(t.TempDir(), "ssh_host_key")
	if err := os.WriteFile(hostKeyFile, []byte("not a private key"), 0o600); err != nil {
		t.Fatalf("write malformed host key: %v", err)
	}

	serverConfig := config.NewDefaultServerConfigForTest()
	serverConfig.HostKeyPath = hostKeyFile
	_, err := New(config.RuntimeConfig{
		Server: serverConfig,
		Common: &config.CommonConfig{SSHPort: 2222, CacheDir: t.TempDir()},
	}, serverTestLoggers, nil)
	if err == nil || !strings.Contains(err.Error(), "parse SSH host key") {
		t.Fatalf("New error = %v, want malformed host-key error", err)
	}
}

func TestStartReturnsListenError(t *testing.T) {
	server := &Server{cfg: config.RuntimeConfig{
		Server: &config.ServerConfig{SSHBindAddress: "127.0.0.1"},
		Common: &config.CommonConfig{SSHPort: 65536},
	}, logger: serverTestLoggers.Diagnostics}

	status, err := server.Start(context.Background())
	if err == nil || status != 1 {
		t.Fatalf("Start status=%d error=%v, want status 1 listen error", status, err)
	}
}
