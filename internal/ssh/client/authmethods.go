package client

import (
	"fmt"
	"io"
	"os"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/ssh"

	gossh "golang.org/x/crypto/ssh"
)

// noopCloser lets callers unconditionally defer closer.Close() when a code
// path does not own a real resource (e.g. no agent available).
type noopCloserFunc struct{}

func (noopCloserFunc) Close() error { return nil }

var noAuthCloser io.Closer = noopCloserFunc{}

var (
	privateKeySigner = ssh.PrivateKeySigner
	agentSigners     = ssh.AgentSignersWithKeyIndex
)

// InitSSHAuthMethods initialises all known SSH auth methods on the client side.
// The returned io.Closer owns any ssh-agent connection acquired while building
// the auth methods and must be closed by the caller once all SSH handshakes
// that consume the returned auth methods have completed. The closer is always
// non-nil so callers can unconditionally `defer closer.Close()`.
func InitSSHAuthMethods(sshAuthMethods []gossh.AuthMethod,
	hostKeyCallback gossh.HostKeyCallback, trustAllHosts bool,
	privateKeyPath string, agentKeyIndex int) ([]gossh.AuthMethod, HostKeyCallback, io.Closer) {

	if len(sshAuthMethods) > 0 {
		simpleCallback, err := NewSimpleCallback()
		if err != nil {
			dlog.Client.FatalPanic(err)
		}
		return sshAuthMethods, simpleCallback, noAuthCloser
	}
	return initKnownHostsAuthMethods(trustAllHosts, privateKeyPath, agentKeyIndex)
}

func initKnownHostsAuthMethods(trustAllHosts bool,
	privateKeyPath string, agentKeyIndex int) ([]gossh.AuthMethod, HostKeyCallback, io.Closer) {

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

	sshAuthMethods, agentCloser := collectKnownHostsAuthMethods(privateKeyPath, agentKeyIndex)
	if len(sshAuthMethods) == 0 {
		_ = agentCloser.Close()
		dlog.Client.FatalPanic("Unable to find private SSH key information")
	}

	return sshAuthMethods, knownHostsCallback, agentCloser
}

func collectKnownHostsAuthMethods(privateKeyPath string, agentKeyIndex int) ([]gossh.AuthMethod, io.Closer) {
	signers, agentCloser := collectKnownHostsSigners(privateKeyPath, agentKeyIndex)
	if len(signers) == 0 {
		return nil, agentCloser
	}
	return []gossh.AuthMethod{gossh.PublicKeys(signers...)}, agentCloser
}

func collectKnownHostsSigners(privateKeyPath string, agentKeyIndex int) ([]gossh.Signer, io.Closer) {
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
	// The agent signers sign lazily over the agent connection, so its
	// io.Closer must live until the caller is done with the signers.
	loadedAgentSigners, agentCloser, err := agentSigners(agentKeyIndex)
	if err != nil {
		dlog.Client.Debug("initKnownHostsAuthMethods", "Unable to load SSH agent signers", err)
	}
	if agentCloser == nil {
		agentCloser = noAuthCloser
	}
	for i, signer := range loadedAgentSigners {
		addSigner(fmt.Sprintf("agent:%d:%d", agentKeyIndex, i), signer)
	}

	// Third, additional default private key paths.
	for _, path := range defaultPrivateKeyPaths {
		addPrivateKeySigner(path)
	}

	return signers, agentCloser
}
