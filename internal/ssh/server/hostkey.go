package server

import (
	"os"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/ssh"
)

const (
	defaultHostKeyBits = 4096
	defaultHostKeyFile = "./cache/ssh_host_key"
)

// PrivateHostKey retrieves the private server RSA host key.
func PrivateHostKey(hostKeyFile string, hostKeyBits int) []byte {
	if hostKeyFile == "" {
		hostKeyFile = defaultHostKeyFile
	}
	if hostKeyBits <= 0 {
		hostKeyBits = defaultHostKeyBits
	}
	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		hostKeyFile = "./ssh_host_key"
	}
	hostKeyPath, err := fs.NewRootedPath(hostKeyFile)
	if err != nil {
		dlog.Server.FatalPanic("Invalid private server RSA host key path", hostKeyFile, err)
	}

	_, err = hostKeyPath.Stat()

	if os.IsNotExist(err) {
		dlog.Server.Info("Generating private server RSA host key")
		pem, err := generatePrivateHostKey(hostKeyBits)
		if err != nil {
			dlog.Server.FatalPanic("Failed to generate private server RSA host key", err)
		}
		if err := storePrivateHostKey(hostKeyPath, pem); err != nil {
			dlog.Server.Error("Unable to write private server RSA host key to file",
				hostKeyFile, err)
		}
		return pem
	}

	dlog.Server.Info("Reading private server RSA host key from file", hostKeyFile)
	pem, err := readPrivateHostKey(hostKeyPath)
	if err != nil {
		dlog.Server.FatalPanic("Failed to load private server RSA host key", err)
	}
	return pem
}

func generatePrivateHostKey(hostKeyBits int) ([]byte, error) {
	privateKey, err := ssh.GeneratePrivateRSAKey(hostKeyBits)
	if err != nil {
		return nil, err
	}

	return ssh.EncodePrivateKeyToPEM(privateKey), nil
}

func storePrivateHostKey(hostKeyPath fs.RootedPath, pem []byte) error {
	return hostKeyPath.WriteFile(pem, 0o600)
}

func readPrivateHostKey(hostKeyPath fs.RootedPath) ([]byte, error) {
	return hostKeyPath.ReadFile()
}
