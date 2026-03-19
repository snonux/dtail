package server

import (
	"bytes"
	"github.com/mimecast/dtail/internal/io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateHostKeyGeneratesAndReloadsExistingKey(t *testing.T) {
	hostKeyFile := filepath.Join(t.TempDir(), "cache", "ssh_host_key")
	if err := os.MkdirAll(filepath.Dir(hostKeyFile), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	hostKeyPath, err := fs.NewRootedPath(hostKeyFile)
	if err != nil {
		t.Fatalf("NewRootedPath failed: %v", err)
	}

	firstPEM, err := generatePrivateHostKey(1024)
	if err != nil {
		t.Fatalf("generatePrivateHostKey failed: %v", err)
	}
	if err := storePrivateHostKey(hostKeyPath, firstPEM); err != nil {
		t.Fatalf("storePrivateHostKey failed: %v", err)
	}

	secondPEM, err := readPrivateHostKey(hostKeyPath)
	if err != nil {
		t.Fatalf("readPrivateHostKey failed: %v", err)
	}
	if !bytes.Equal(secondPEM, firstPEM) {
		t.Fatalf("readPrivateHostKey returned different key data")
	}
}
