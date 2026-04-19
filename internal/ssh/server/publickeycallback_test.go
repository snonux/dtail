package server

import (
	"bytes"
	"errors"
	"os"
	goUser "os/user"
	"path/filepath"
	"testing"
	"time"

	serveruser "github.com/mimecast/dtail/internal/user/server"

	gossh "golang.org/x/crypto/ssh"
)

func TestAuthKeyStorePermissions(t *testing.T) {
	// Create an isolated store for this test — there is no package-level global.
	store := NewAuthKeyStore(time.Hour, 5)

	key := testPublicKey(t, 21)

	if permissions := authKeyStorePermissions(store, "alice", key); permissions != nil {
		t.Fatalf("Expected nil permissions when no key is cached")
	}

	store.Add("alice", key)

	permissions := authKeyStorePermissions(store, "alice", key)
	if permissions == nil {
		t.Fatalf("Expected permissions when key is cached")
	}
	if fingerprint := permissions.Extensions["pubkey-fp"]; fingerprint != gossh.FingerprintSHA256(key) {
		t.Fatalf("Unexpected fingerprint: %s", fingerprint)
	}

	if permissions := authKeyStorePermissions(store, "bob", key); permissions != nil {
		t.Fatalf("Expected nil permissions for different user")
	}

	unknownKey := testPublicKey(t, 22)
	if permissions := authKeyStorePermissions(store, "alice", unknownKey); permissions != nil {
		t.Fatalf("Expected nil permissions for unknown key")
	}
}

func TestVerifyAuthorizedKeysSkipsMalformedLineWithoutParserProgress(t *testing.T) {
	user := testServerUser(t, "alice")
	firstKey := testPublicKey(t, 41)
	secondKey := testPublicKey(t, 42)

	firstLine := gossh.MarshalAuthorizedKey(firstKey)
	badLine := []byte("this is not an authorized key\n")
	secondLine := gossh.MarshalAuthorizedKey(secondKey)
	authorizedKeys := append(append(append([]byte{}, firstLine...), badLine...), secondLine...)

	parser := func(in []byte) (gossh.PublicKey, string, []string, []byte, error) {
		switch {
		case bytes.HasPrefix(in, firstLine):
			return firstKey, "", nil, in[len(firstLine):], nil
		case bytes.HasPrefix(in, badLine):
			return nil, "", nil, in, errors.New("parse error")
		case bytes.HasPrefix(in, secondLine):
			return secondKey, "", nil, in[len(secondLine):], nil
		default:
			return nil, "", nil, nil, errors.New("unexpected authorized_keys input")
		}
	}

	permissions, err := verifyAuthorizedKeysWithParser(user, authorizedKeys, secondKey, parser)
	if err != nil {
		t.Fatalf("verifyAuthorizedKeysWithParser failed: %v", err)
	}
	if permissions == nil {
		t.Fatalf("Expected permissions for key after malformed line")
	}
	if got := permissions.Extensions["pubkey-fp"]; got != gossh.FingerprintSHA256(secondKey) {
		t.Fatalf("Unexpected fingerprint: %s", got)
	}
}

func TestVerifyAuthorizedKeysSkipsMalformedLineWithRealParser(t *testing.T) {
	user := testServerUser(t, "alice")
	firstKey := testPublicKey(t, 43)
	secondKey := testPublicKey(t, 44)

	badLine := []byte("ssh-rsa !!!!\n")
	authorizedKeys := append(append(append([]byte{}, gossh.MarshalAuthorizedKey(firstKey)...),
		badLine...), gossh.MarshalAuthorizedKey(secondKey)...)

	sawParseError := false
	parseAuthorizedKeyLineByLine := func(in []byte) (gossh.PublicKey, string, []string, []byte, error) {
		line := in
		rest := []byte(nil)
		if lineEnd := bytes.IndexByte(in, '\n'); lineEnd >= 0 {
			line = in[:lineEnd+1]
			rest = in[lineEnd+1:]
		}

		authorizedPubKey, comment, options, _, err := gossh.ParseAuthorizedKey(line)
		if err != nil {
			if bytes.Equal(line, badLine) {
				sawParseError = true
			}
			return nil, "", nil, rest, err
		}

		return authorizedPubKey, comment, options, rest, nil
	}

	permissions, err := verifyAuthorizedKeysWithParser(user, authorizedKeys, secondKey, parseAuthorizedKeyLineByLine)
	if err != nil {
		t.Fatalf("verifyAuthorizedKeysWithParser failed: %v", err)
	}
	if permissions == nil {
		t.Fatalf("Expected permissions for key after malformed line")
	}
	if got := permissions.Extensions["pubkey-fp"]; got != gossh.FingerprintSHA256(secondKey) {
		t.Fatalf("Unexpected fingerprint: %s", got)
	}
	if !sawParseError {
		t.Fatalf("Expected malformed authorized_keys line to hit the real parser error path")
	}
}

func TestFindAuthorizedKeysPathUsesCacheDirWhenPresent(t *testing.T) {
	cwd := t.TempDir()
	cacheDir := "cache"
	user := testServerUser(t, "alice")
	wantPath := filepath.Join(cwd, cacheDir, "alice.authorized_keys")
	if err := os.MkdirAll(filepath.Dir(wantPath), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	want := gossh.MarshalAuthorizedKey(testPublicKey(t, 31))
	if err := os.WriteFile(wantPath, want, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	rootedPath, err := findAuthorizedKeysPath(user, cacheDir, cwd, func(string) (*goUser.User, error) {
		t.Fatalf("lookupUser should not be called when cached authorized_keys exists")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("findAuthorizedKeysPath failed: %v", err)
	}
	if rootedPath.Path() != wantPath {
		t.Fatalf("findAuthorizedKeysPath returned %q, want %q", rootedPath.Path(), wantPath)
	}

	got, err := rootedPath.ReadFile()
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadFile returned %q, want %q", got, want)
	}
}

func TestFindAuthorizedKeysPathFallsBackToHomeAuthorizedKeys(t *testing.T) {
	cwd := t.TempDir()
	homeDir := t.TempDir()
	user := testServerUser(t, "alice")
	wantPath := filepath.Join(homeDir, ".ssh", "authorized_keys")
	if err := os.MkdirAll(filepath.Dir(wantPath), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	want := gossh.MarshalAuthorizedKey(testPublicKey(t, 32))
	if err := os.WriteFile(wantPath, want, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	rootedPath, err := findAuthorizedKeysPath(user, "cache", cwd, func(name string) (*goUser.User, error) {
		return &goUser.User{Username: name, HomeDir: homeDir}, nil
	})
	if err != nil {
		t.Fatalf("findAuthorizedKeysPath failed: %v", err)
	}
	if rootedPath.Path() != wantPath {
		t.Fatalf("findAuthorizedKeysPath returned %q, want %q", rootedPath.Path(), wantPath)
	}

	got, err := rootedPath.ReadFile()
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadFile returned %q, want %q", got, want)
	}
}

func TestFindAuthorizedKeysPathRejectsEscapingHomeSymlink(t *testing.T) {
	cwd := t.TempDir()
	homeDir := t.TempDir()
	user := testServerUser(t, "alice")
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	outsidePath := filepath.Join(homeDir, "outside_authorized_keys")
	if err := os.WriteFile(outsidePath, gossh.MarshalAuthorizedKey(testPublicKey(t, 33)), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "outside_authorized_keys"),
		filepath.Join(sshDir, "authorized_keys")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	_, err := findAuthorizedKeysPath(user, "", cwd, func(name string) (*goUser.User, error) {
		return &goUser.User{Username: name, HomeDir: homeDir}, nil
	})
	if err == nil {
		t.Fatalf("findAuthorizedKeysPath succeeded for escaping authorized_keys symlink")
	}
}

func testServerUser(t *testing.T, name string) *serveruser.User {
	t.Helper()

	user, err := serveruser.New(name, "127.0.0.1:2222", nil)
	if err != nil {
		t.Fatalf("serveruser.New failed: %v", err)
	}
	return user
}
