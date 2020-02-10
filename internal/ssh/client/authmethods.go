package client

import (
	"os"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/logger"
	"github.com/mimecast/dtail/internal/ssh"

	gossh "golang.org/x/crypto/ssh"
)

// InitSSHAuthMethods initialises all known SSH auth methods on othe client side.
func InitSSHAuthMethods(args clients.Args, trustAllHosts bool, throttleCh chan struct{}) ([]gossh.AuthMethod, *HostKeyCallback) {
	if len(args.SSHAuthMethods) > 0 {
		hostKeyCallback, err := NewSimpleCallback(trustAllHosts)
		if err != nil {
			logger.FatalExit(err)
		}
		return args.SSHAuthMethods, hostKeyCallback
	}

	var sshAuthMethods []gossh.AuthMethod
	if config.Common.ExperimentalFeaturesEnable {
		sshAuthMethods = append(sshAuthMethods, gossh.Password("experimental feature test"))
		logger.Info("Added experimental method to list of auth methods")
	}

	keyPath := os.Getenv("HOME") + "/.ssh/id_rsa"
	if authMethod, err := ssh.PrivateKey(keyPath); err == nil {
		sshAuthMethods = append(sshAuthMethods, authMethod)
		logger.Info("Added path to list of auth methods", keyPath)
	}

	keyPath = os.Getenv("HOME") + "/.ssh/id_dsa"
	if authMethod, err := ssh.PrivateKey(keyPath); err == nil {
		sshAuthMethods = append(sshAuthMethods, authMethod)
		logger.Info("Added path to list of auth methods", keyPath)
	}

	if authMethod, err := ssh.Agent(); err == nil {
		sshAuthMethods = append(sshAuthMethods, authMethod)
		logger.Info("Added SSH Agent to list of auth methods")
	}

	knownHostsPath := os.Getenv("HOME") + "/.ssh/known_hosts"
	hostKeyCallback, err := NewHostKeyCallback(knownHostsPath, trustAllHosts, throttleCh)
	if err != nil {
		logger.FatalExit(knownHostsPath, err)
	}

	return sshAuthMethods, hostKeyCallback
}
