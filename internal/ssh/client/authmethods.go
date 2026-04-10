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
	privateKeySigner = ssh.PrivateKeySigner
	agentSigners     = ssh.AgentSignersWithKeyIndex
)

// InitSSHAuthMethods initialises all known SSH auth methods on the client side.
func InitSSHAuthMethods(sshAuthMethods []gossh.AuthMethod,
	hostKeyCallback gossh.HostKeyCallback, trustAllHosts bool,
	privateKeyPath string, agentKeyIndex int) ([]gossh.AuthMethod, HostKeyCallback) {

	if len(sshAuthMethods) > 0 {
		simpleCallback, err := NewSimpleCallback()
		if err != nil {
			dlog.Client.FatalPanic(err)
		}
		return sshAuthMethods, simpleCallback
	}
	return initKnownHostsAuthMethods(trustAllHosts, privateKeyPath, agentKeyIndex)
}

func initKnownHostsAuthMethods(trustAllHosts bool,
	privateKeyPath string, agentKeyIndex int) ([]gossh.AuthMethod, HostKeyCallback) {

	knownHostsFile := fmt.Sprintf("%s/.ssh/known_hosts", os.Getenv("HOME"))
	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		// In case of integration test, override known hosts file path.
		knownHostsFile = "./known_hosts"
	}

	knownHostsCallback, err := NewKnownHostsCallback(knownHostsFile, trustAllHosts)
	if err != nil {
		dlog.Client.FatalPanic(knownHostsFile, err)
	}
	dlog.Client.Debug("initKnownHostsAuthMethods", "Added known hosts file path", knownHostsFile)

	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		if privateKeyPath == "" {
			privateKeyPath = "./id_rsa"
		}
		GeneratePrivatePublicKeyPairIfNotExists(privateKeyPath, 4096)
	}

	sshAuthMethods := collectKnownHostsAuthMethods(privateKeyPath, agentKeyIndex)
	if len(sshAuthMethods) == 0 {
		dlog.Client.FatalPanic("Unable to find private SSH key information")
	}

	return sshAuthMethods, knownHostsCallback
}

func collectKnownHostsAuthMethods(privateKeyPath string, agentKeyIndex int) []gossh.AuthMethod {
	signers := collectKnownHostsSigners(privateKeyPath, agentKeyIndex)
	if len(signers) == 0 {
		return nil
	}
	return []gossh.AuthMethod{gossh.PublicKeys(signers...)}
}

func collectKnownHostsSigners(privateKeyPath string, agentKeyIndex int) []gossh.Signer {
	var signers []gossh.Signer

	home := os.Getenv("HOME")
	defaultPrivateKeyPaths := []string{
		home + "/.ssh/id_rsa",
		home + "/.ssh/id_dsa",
		home + "/.ssh/id_ecdsa",
		home + "/.ssh/id_ed25519",
	}
	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		defaultPrivateKeyPaths = append([]string{"./id_rsa"}, defaultPrivateKeyPaths...)
	}

	if privateKeyPath == "" {
		privateKeyPath = defaultPrivateKeyPaths[0]
	}

	addedPrivateKeyPaths := make(map[string]bool, len(defaultPrivateKeyPaths)+1)
	addedPublicKeys := make(map[string]bool, len(defaultPrivateKeyPaths)+1)
	addSigner := func(source string, signer gossh.Signer) {
		if signer == nil {
			return
		}

		pubKey := string(signer.PublicKey().Marshal())
		if addedPublicKeys[pubKey] {
			dlog.Client.Debug("initKnownHostsAuthMethods", "Skipping duplicate signer", source)
			return
		}

		addedPublicKeys[pubKey] = true
		signers = append(signers, signer)
		dlog.Client.Debug("initKnownHostsAuthMethods", "Added signer", source)
	}
	addPrivateKeySigner := func(path string) {
		if path == "" {
			return
		}
		if addedPrivateKeyPaths[path] {
			return
		}

		signer, err := privateKeySigner(path)
		if err != nil {
			dlog.Client.Debug("initKnownHostsAuthMethods", "Unable to load private key signer", path, err)
			return
		}

		addedPrivateKeyPaths[path] = true
		addSigner(path, signer)
	}

	// First, the explicit auth key path (or default ~/.ssh/id_rsa).
	addPrivateKeySigner(privateKeyPath)

	// Second, SSH agent (YubiKey-backed keys are typically exposed here).
	loadedAgentSigners, err := agentSigners(agentKeyIndex)
	if err == nil {
		for i, signer := range loadedAgentSigners {
			addSigner(fmt.Sprintf("agent:%d:%d", agentKeyIndex, i), signer)
		}
	} else {
		dlog.Client.Debug("initKnownHostsAuthMethods", "Unable to load SSH agent signers", err)
	}

	// Third, additional default private key paths.
	for _, path := range defaultPrivateKeyPaths {
		addPrivateKeySigner(path)
	}

	return signers
}
