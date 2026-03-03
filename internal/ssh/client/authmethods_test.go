package client

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/mimecast/dtail/internal/io/dlog"

	gossh "golang.org/x/crypto/ssh"
)

func TestCollectKnownHostsAuthMethodsOrder(t *testing.T) {
	homeDir := "/tmp/dtail-auth-order"
	t.Setenv("HOME", homeDir)

	originalPrivateKeyAuthMethod := privateKeyAuthMethod
	originalAgentAuthMethod := agentAuthMethod
	originalLogger := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() {
		privateKeyAuthMethod = originalPrivateKeyAuthMethod
		agentAuthMethod = originalAgentAuthMethod
		dlog.Client = originalLogger
	})

	var callOrder []string
	successfulPrivateKeys := map[string]bool{
		"/custom/id_fast":        true,
		homeDir + "/.ssh/id_rsa": true,
		homeDir + "/.ssh/id_dsa": true,
	}

	privateKeyAuthMethod = func(path string) (gossh.AuthMethod, error) {
		callOrder = append(callOrder, "private:"+path)
		if !successfulPrivateKeys[path] {
			return nil, fmt.Errorf("missing private key: %s", path)
		}
		return gossh.Password(path), nil
	}
	agentAuthMethod = func(keyIndex int) (gossh.AuthMethod, error) {
		callOrder = append(callOrder, fmt.Sprintf("agent:%d", keyIndex))
		return gossh.Password("agent"), nil
	}

	methods := collectKnownHostsAuthMethods("/custom/id_fast", 7)
	if len(methods) != 4 {
		t.Fatalf("Expected 4 auth methods, got %d", len(methods))
	}

	expectedOrder := []string{
		"private:/custom/id_fast",
		"agent:7",
		"private:/tmp/dtail-auth-order/.ssh/id_rsa",
		"private:/tmp/dtail-auth-order/.ssh/id_dsa",
		"private:/tmp/dtail-auth-order/.ssh/id_ecdsa",
		"private:/tmp/dtail-auth-order/.ssh/id_ed25519",
	}
	if !reflect.DeepEqual(callOrder, expectedOrder) {
		t.Fatalf("Unexpected auth method call order.\nexpected: %v\ngot:      %v", expectedOrder, callOrder)
	}
}

func TestCollectKnownHostsAuthMethodsSkipsDuplicateDefaultPath(t *testing.T) {
	homeDir := "/tmp/dtail-auth-dedupe"
	t.Setenv("HOME", homeDir)

	originalPrivateKeyAuthMethod := privateKeyAuthMethod
	originalAgentAuthMethod := agentAuthMethod
	originalLogger := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() {
		privateKeyAuthMethod = originalPrivateKeyAuthMethod
		agentAuthMethod = originalAgentAuthMethod
		dlog.Client = originalLogger
	})

	var callOrder []string
	privateKeyAuthMethod = func(path string) (gossh.AuthMethod, error) {
		callOrder = append(callOrder, "private:"+path)
		if path == homeDir+"/.ssh/id_rsa" {
			return gossh.Password(path), nil
		}
		return nil, fmt.Errorf("missing private key: %s", path)
	}
	agentAuthMethod = func(keyIndex int) (gossh.AuthMethod, error) {
		callOrder = append(callOrder, fmt.Sprintf("agent:%d", keyIndex))
		return gossh.Password("agent"), nil
	}

	methods := collectKnownHostsAuthMethods(homeDir+"/.ssh/id_rsa", 2)
	if len(methods) != 2 {
		t.Fatalf("Expected 2 auth methods, got %d", len(methods))
	}

	expectedOrder := []string{
		"private:/tmp/dtail-auth-dedupe/.ssh/id_rsa",
		"agent:2",
		"private:/tmp/dtail-auth-dedupe/.ssh/id_dsa",
		"private:/tmp/dtail-auth-dedupe/.ssh/id_ecdsa",
		"private:/tmp/dtail-auth-dedupe/.ssh/id_ed25519",
	}
	if !reflect.DeepEqual(callOrder, expectedOrder) {
		t.Fatalf("Unexpected auth method call order.\nexpected: %v\ngot:      %v", expectedOrder, callOrder)
	}
}
