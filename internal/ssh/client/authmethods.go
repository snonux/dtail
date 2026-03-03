package client

import (
	"fmt"
	"os"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/ssh"

	gossh "golang.org/x/crypto/ssh"
)

var (
	privateKeyAuthMethod = ssh.PrivateKey
	agentAuthMethod      = ssh.AgentWithKeyIndex
)

// InitSSHAuthMethods initialises all known SSH auth methods on the client side.
func InitSSHAuthMethods(sshAuthMethods []gossh.AuthMethod,
	hostKeyCallback gossh.HostKeyCallback, trustAllHosts bool, throttleCh chan struct{},
	privateKeyPath string, agentKeyIndex int) ([]gossh.AuthMethod, HostKeyCallback) {

	if len(sshAuthMethods) > 0 {
		simpleCallback, err := NewSimpleCallback()
		if err != nil {
			dlog.Client.FatalPanic(err)
		}
		return sshAuthMethods, simpleCallback
	}
	return initKnownHostsAuthMethods(trustAllHosts, throttleCh, privateKeyPath, agentKeyIndex)
}

func initIntegrationTestKnownHostsAuthMethods() []gossh.AuthMethod {
	var sshAuthMethods []gossh.AuthMethod
	privateKeyPath := "./id_rsa"

	GeneratePrivatePublicKeyPairIfNotExists(privateKeyPath, 4096)
	authMethod, err := ssh.PrivateKey(privateKeyPath)
	if err != nil {
		dlog.Client.FatalPanic("Unable to use private SSH key", privateKeyPath, err)
	}

	sshAuthMethods = append(sshAuthMethods, authMethod)
	dlog.Client.Debug("initKnownHostsAuthMethods", "Added private key auth method", privateKeyPath)
	return sshAuthMethods
}

func initKnownHostsAuthMethods(trustAllHosts bool, throttleCh chan struct{},
	privateKeyPath string, agentKeyIndex int) ([]gossh.AuthMethod, HostKeyCallback) {

	knownHostsFile := fmt.Sprintf("%s/.ssh/known_hosts", os.Getenv("HOME"))
	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		// In case of integration test, override known hosts file path.
		knownHostsFile = "./known_hosts"
	}

	knownHostsCallback, err := NewKnownHostsCallback(knownHostsFile, trustAllHosts, throttleCh)
	if err != nil {
		dlog.Client.FatalPanic(knownHostsFile, err)
	}
	dlog.Client.Debug("initKnownHostsAuthMethods", "Added known hosts file path", knownHostsFile)

	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		return initIntegrationTestKnownHostsAuthMethods(), knownHostsCallback
	}

	sshAuthMethods := collectKnownHostsAuthMethods(privateKeyPath, agentKeyIndex)
	if len(sshAuthMethods) == 0 {
		dlog.Client.FatalPanic("Unable to find private SSH key information")
	}

	return sshAuthMethods, knownHostsCallback
}

func collectKnownHostsAuthMethods(privateKeyPath string, agentKeyIndex int) []gossh.AuthMethod {
	var sshAuthMethods []gossh.AuthMethod

	home := os.Getenv("HOME")
	defaultPrivateKeyPaths := []string{
		home + "/.ssh/id_rsa",
		home + "/.ssh/id_dsa",
		home + "/.ssh/id_ecdsa",
		home + "/.ssh/id_ed25519",
	}

	if privateKeyPath == "" {
		privateKeyPath = defaultPrivateKeyPaths[0]
	}

	addedPrivateKeyPaths := make(map[string]bool, len(defaultPrivateKeyPaths)+1)
	addPrivateKeyAuthMethod := func(path string) {
		if path == "" {
			return
		}
		if addedPrivateKeyPaths[path] {
			return
		}

		authMethod, err := privateKeyAuthMethod(path)
		if err != nil {
			dlog.Client.Debug("initKnownHostsAuthMethods", "Unable to use private key", path, err)
			return
		}

		sshAuthMethods = append(sshAuthMethods, authMethod)
		addedPrivateKeyPaths[path] = true
		dlog.Client.Debug("initKnownHostsAuthMethods", "Added private key auth method", path)
	}

	// First, the explicit auth key path (or default ~/.ssh/id_rsa).
	addPrivateKeyAuthMethod(privateKeyPath)

	// Second, SSH agent (YubiKey-backed keys are typically exposed here).
	authMethod, err := agentAuthMethod(agentKeyIndex)
	if err == nil {
		sshAuthMethods = append(sshAuthMethods, authMethod)
		dlog.Client.Debug("initKnownHostsAuthMethods", "Added SSH agent auth method")
	} else {
		dlog.Client.Debug("initKnownHostsAuthMethods", "Unable to init SSH Agent auth method", err)
	}

	// Third, additional default private key paths.
	for _, path := range defaultPrivateKeyPaths {
		addPrivateKeyAuthMethod(path)
	}

	return sshAuthMethods
}
