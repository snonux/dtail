package server

import (
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func TestAuthKeyStorePermissions(t *testing.T) {
	previousStore := authKeyStore
	authKeyStore = NewAuthKeyStore(time.Hour, 5)
	t.Cleanup(func() {
		authKeyStore = previousStore
	})

	key := testPublicKey(t, 21)

	if permissions := authKeyStorePermissions("alice", key); permissions != nil {
		t.Fatalf("Expected nil permissions when no key is cached")
	}

	authKeyStore.Add("alice", key)

	permissions := authKeyStorePermissions("alice", key)
	if permissions == nil {
		t.Fatalf("Expected permissions when key is cached")
	}
	if fingerprint := permissions.Extensions["pubkey-fp"]; fingerprint != gossh.FingerprintSHA256(key) {
		t.Fatalf("Unexpected fingerprint: %s", fingerprint)
	}

	if permissions := authKeyStorePermissions("bob", key); permissions != nil {
		t.Fatalf("Expected nil permissions for different user")
	}

	unknownKey := testPublicKey(t, 22)
	if permissions := authKeyStorePermissions("alice", unknownKey); permissions != nil {
		t.Fatalf("Expected nil permissions for unknown key")
	}
}
