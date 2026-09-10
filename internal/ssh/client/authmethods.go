package client

import (
	"fmt"
	"io"
	"os"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/logging"
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
// that consume the returned auth methods have completed. On success the closer
// is always non-nil so callers can unconditionally defer closer.Close().
func InitSSHAuthMethods(sshAuthMethods []gossh.AuthMethod,
	hostKeyCallback gossh.HostKeyCallback, trustAllHosts bool,
	privateKeyPath string, agentKeyIndex int, logger,
	promptLogger logging.Logger) ([]gossh.AuthMethod, HostKeyCallback, io.Closer, error) {

	logger = logging.OrNop(logger)
	if len(sshAuthMethods) > 0 {
		simpleCallback, err := NewSimpleCallback()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("initialize SSH host-key callback: %w", err)
		}
		return sshAuthMethods, simpleCallback, noAuthCloser, nil
	}
	return initKnownHostsAuthMethods(trustAllHosts, privateKeyPath, agentKeyIndex, logger, promptLogger)
}

func initKnownHostsAuthMethods(trustAllHosts bool,
	privateKeyPath string, agentKeyIndex int, logger,
	promptLogger logging.Logger) ([]gossh.AuthMethod, HostKeyCallback, io.Closer, error) {

	knownHostsFile := fmt.Sprintf("%s/.ssh/known_hosts", os.Getenv("HOME"))
	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		// In case of integration test, override known hosts file path.
		knownHostsFile = "./known_hosts"
	}

	knownHostsCallback, err := NewKnownHostsCallback(knownHostsFile, trustAllHosts, logger, promptLogger)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("initialize known-hosts callback from %q: %w", knownHostsFile, err)
	}
	logger.Debug("initKnownHostsAuthMethods", "Added known hosts file path", knownHostsFile)

	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		if privateKeyPath == "" {
			privateKeyPath = config.IntegrationSSHPrivateKeyPath()
		}
	}

	sshAuthMethods, agentCloser, err := collectKnownHostsAuthMethods(privateKeyPath, agentKeyIndex, logger)
	if err != nil {
		return nil, nil, nil, err
	}

	return sshAuthMethods, knownHostsCallback, agentCloser, nil
}

func collectKnownHostsAuthMethods(privateKeyPath string, agentKeyIndex int,
	logger logging.Logger) ([]gossh.AuthMethod, io.Closer, error) {
	signers, agentCloser := collectKnownHostsSigners(privateKeyPath, agentKeyIndex, logger)
	if len(signers) == 0 {
		if err := agentCloser.Close(); err != nil {
			return nil, nil, fmt.Errorf("close SSH agent after authentication setup failure: %w", err)
		}
		if privateKeyPath != "" {
			return nil, nil, fmt.Errorf("unable to load SSH private key %q or find an SSH agent key", privateKeyPath)
		}
		return nil, nil, fmt.Errorf("unable to find a usable SSH private key or SSH agent key")
	}
	return []gossh.AuthMethod{gossh.PublicKeys(signers...)}, agentCloser, nil
}

func collectKnownHostsSigners(privateKeyPath string, agentKeyIndex int,
	logger logging.Logger) ([]gossh.Signer, io.Closer) {
	var signers []gossh.Signer

	home := os.Getenv("HOME")
	defaultPrivateKeyPaths := []string{
		home + "/.ssh/id_rsa",
		home + "/.ssh/id_dsa",
		home + "/.ssh/id_ecdsa",
		home + "/.ssh/id_ed25519",
	}
	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		defaultPrivateKeyPaths = append([]string{config.IntegrationSSHPrivateKeyPath()}, defaultPrivateKeyPaths...)
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
			logger.Debug("initKnownHostsAuthMethods", "Skipping duplicate signer", source)
			return
		}

		addedPublicKeys[pubKey] = true
		signers = append(signers, signer)
		logger.Debug("initKnownHostsAuthMethods", "Added signer", source)
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
			logger.Debug("initKnownHostsAuthMethods", "Unable to load private key signer", path, err)
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
	loadedAgentSigners, agentCloser, err := agentSigners(agentKeyIndex, logger)
	if err != nil {
		logger.Debug("initKnownHostsAuthMethods", "Unable to load SSH agent signers", err)
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
