package ssh

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"syscall"

	"github.com/mimecast/dtail/internal/io/dlog"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// GeneratePrivateRSAKey is used by the server to generate its key.
func GeneratePrivateRSAKey(size int) (*rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, size)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}
	if err = privateKey.Validate(); err != nil {
		return nil, fmt.Errorf("failed to validate generated RSA key: %w", err)
	}
	return privateKey, nil
}

// EncodePrivateKeyToPEM is a helper function for converting a key to PEM format.
func EncodePrivateKeyToPEM(privateKey *rsa.PrivateKey) []byte {
	derFormat := x509.MarshalPKCS1PrivateKey(privateKey)

	block := pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   derFormat,
	}
	return pem.EncodeToMemory(&block)
}

// Agent used for SSH auth.
func Agent() (gossh.AuthMethod, error) {
	return AgentWithKeyIndex(-1)
}

// AgentWithKeyIndex used for SSH auth with a specific key index from the agent.
// If keyIndex is -1, all keys are used. Otherwise, only the specified key is used.
func AgentWithKeyIndex(keyIndex int) (gossh.AuthMethod, error) {
	sshAgent, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SSH agent: %w", err)
	}
	agentClient := agent.NewClient(sshAgent)
	keys, err := agentClient.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list SSH agent keys: %w", err)
	}
	for i, key := range keys {
		dlog.Common.Debug("Public key", i, key)
	}
	
	// If no specific key index requested, use all keys (backwards compatible default)
	if keyIndex < 0 {
		return gossh.PublicKeysCallback(agentClient.Signers), nil
	}
	
	// Use only the specified key index (0-based)
	if keyIndex >= len(keys) {
		return nil, fmt.Errorf("key index %d out of range (agent has %d keys)", keyIndex, len(keys))
	}
	
	dlog.Common.Debug("Using SSH agent key at index", keyIndex)
	return gossh.PublicKeysCallback(func() ([]gossh.Signer, error) {
		signers, err := agentClient.Signers()
		if err != nil {
			return nil, err
		}
		if keyIndex >= len(signers) {
			return nil, fmt.Errorf("key index %d out of range (agent has %d signers)", keyIndex, len(signers))
		}
		// Return only the specified signer
		return []gossh.Signer{signers[keyIndex]}, nil
	}), nil
}

// EnterKeyPhrase is required to read phrase protected private keys.
func EnterKeyPhrase(keyFile string) []byte {
	fmt.Printf("Enter phrase for key %s: ", keyFile)
	phrase, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s\n", string(phrase))
	return phrase
}

// KeyFile returns the key as a SSH auth method.
func KeyFile(keyFile string) (gossh.AuthMethod, error) {
	buffer, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	key, err := gossh.ParsePrivateKey(buffer)
	if err != nil {
		return nil, err
	}

	// Key phrase support disabled as password will be printed to stdout!
	/*
		if err == nil {
			return gossh.PublicKeys(key), nil
		}

		keyPhrase := EnterKeyPhrase(keyFile)
		key, err = gossh.ParsePrivateKeyWithPassphrase(buffer, keyPhrase)
		if err != nil {
			return nil, err
		}
	*/

	return gossh.PublicKeys(key), nil
}

// PrivateKey returns the private key as a SSH auth method.
func PrivateKey(keyFile string) (gossh.AuthMethod, error) {
	signer, err := KeyFile(keyFile)
	if err != nil {
		dlog.Common.Debug(keyFile, err)
		return nil, err
	}
	return gossh.AuthMethod(signer), nil
}
