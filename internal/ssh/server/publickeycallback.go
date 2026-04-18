package server

import (
	"bytes"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	goUser "os/user"
	"path/filepath"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/fs"
	user "github.com/mimecast/dtail/internal/user/server"

	gossh "golang.org/x/crypto/ssh"
)

type authorizedKeyParser func([]byte) (gossh.PublicKey, string, []string, []byte, error)

// NewPublicKeyCallback creates an instance-scoped SSH public key callback.
// It avoids relying on package-level mutable configuration/state.
func NewPublicKeyCallback(authKeyEnabled bool, cacheDir string,
	keyStore *AuthKeyStore) func(gossh.ConnMetadata, gossh.PublicKey) (*gossh.Permissions, error) {

	if keyStore == nil {
		keyStore = authKeyStore
	}
	return func(c gossh.ConnMetadata, offeredPubKey gossh.PublicKey) (*gossh.Permissions, error) {
		return publicKeyCallback(c, offeredPubKey, authKeyEnabled, cacheDir, keyStore)
	}
}

func publicKeyCallback(c gossh.ConnMetadata, offeredPubKey gossh.PublicKey,
	authKeyEnabled bool, cacheDir string, keyStore *AuthKeyStore) (*gossh.Permissions, error) {

	user, err := user.New(c.User(), c.RemoteAddr().String(), nil)
	if err != nil {
		return nil, err
	}
	dlog.Server.Info(user, "Incoming authorization")

	if authKeyEnabled {
		if permissions := authKeyStorePermissions(keyStore, user.Name, offeredPubKey); permissions != nil {
			dlog.Server.Info(user, "Authorized by in-memory auth key store")
			return permissions, nil
		}
	}

	authorizedKeysPath, err := authorizedKeysPathForUser(user, cacheDir)
	if err != nil {
		return nil, err
	}

	dlog.Server.Info(user, "Reading", authorizedKeysPath.Path())
	authorizedKeysBytes, err := authorizedKeysPath.ReadFile()
	if err != nil {
		return nil, fmt.Errorf("Unable to read authorized keys file|%s|%s|%s",
			authorizedKeysPath.Path(), user, err.Error())
	}

	return verifyAuthorizedKeys(user, authorizedKeysBytes, offeredPubKey)
}

func verifyAuthorizedKeys(user *user.User, authorizedKeysBytes []byte,
	offeredPubKey gossh.PublicKey) (*gossh.Permissions, error) {
	return verifyAuthorizedKeysWithParser(user, authorizedKeysBytes, offeredPubKey, gossh.ParseAuthorizedKey)
}

func verifyAuthorizedKeysWithParser(user *user.User, authorizedKeysBytes []byte,
	offeredPubKey gossh.PublicKey, parseAuthorizedKey authorizedKeyParser) (*gossh.Permissions, error) {

	authorizedKeysMap := map[string]bool{}
	for len(authorizedKeysBytes) > 0 {
		authorizedPubKey, _, _, restBytes, err := parseAuthorizedKey(authorizedKeysBytes)
		if err != nil {
			if dlog.Server != nil {
				dlog.Server.Warn(user, "Skipping unparseable authorized_keys line", err)
			}
			nextAuthorizedKeysBytes, ok := advanceToNextAuthorizedKeysLine(authorizedKeysBytes)
			if !ok {
				break
			}
			authorizedKeysBytes = nextAuthorizedKeysBytes
			continue
		}
		authorizedKeysMap[string(authorizedPubKey.Marshal())] = true
		authorizedKeysBytes = restBytes
		if dlog.Server != nil {
			dlog.Server.Debug(user, "Authorized public key fingerprint",
				gossh.FingerprintSHA256(authorizedPubKey))
		}
	}

	if dlog.Server != nil {
		dlog.Server.Debug(user, "Offered public key fingerprint", gossh.FingerprintSHA256(offeredPubKey))
	}
	if authorizedKeysMap[string(offeredPubKey.Marshal())] {
		return permissionsFromPublicKey(offeredPubKey), nil
	}

	return nil, fmt.Errorf("%s|public key of user not authorized", user)
}

func advanceToNextAuthorizedKeysLine(authorizedKeysBytes []byte) ([]byte, bool) {
	lineEnd := bytes.IndexByte(authorizedKeysBytes, '\n')
	if lineEnd == -1 {
		return nil, false
	}

	nextBytes := authorizedKeysBytes[lineEnd+1:]
	return nextBytes, true
}

func authKeyStorePermissions(keyStore *AuthKeyStore, userName string,
	offeredPubKey gossh.PublicKey) *gossh.Permissions {

	if keyStore == nil || !keyStore.Has(userName, offeredPubKey) {
		return nil
	}

	return permissionsFromPublicKey(offeredPubKey)
}

func permissionsFromPublicKey(offeredPubKey gossh.PublicKey) *gossh.Permissions {
	return &gossh.Permissions{
		Extensions: map[string]string{"pubkey-fp": gossh.FingerprintSHA256(offeredPubKey)},
	}
}

type userLookupFunc func(string) (*goUser.User, error)

func authorizedKeysPathForUser(user *user.User, cacheDir string) (fs.RootedPath, error) {
	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		// In this case, we expect a pub key in the current directory.
		return fs.NewRootedPath("./id_rsa.pub")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fs.RootedPath{}, err
	}

	return findAuthorizedKeysPath(user, cacheDir, cwd, goUser.Lookup)
}

func findAuthorizedKeysPath(user *user.User, cacheDir, cwd string,
	lookupUser userLookupFunc) (fs.RootedPath, error) {

	// Check for cached version in the dserver directory.
	if cacheDir != "" {
		cachePath := filepath.Join(cwd, cacheDir, fmt.Sprintf("%s.authorized_keys", user.Name))
		rootedCachePath, err := fs.NewRootedPath(cachePath)
		if err != nil {
			return fs.RootedPath{}, err
		}
		if _, err := rootedCachePath.Stat(); err == nil {
			return rootedCachePath, nil
		}
	}

	// As the last option, check the regular SSH path.
	osUser, err := lookupUser(user.Name)
	if err != nil {
		return fs.RootedPath{}, err
	}
	authorizedKeysPath := filepath.Join(osUser.HomeDir, ".ssh", "authorized_keys")
	rootedAuthorizedKeysPath, err := fs.NewRootedPath(authorizedKeysPath)
	if err != nil {
		return fs.RootedPath{}, err
	}
	if _, err = rootedAuthorizedKeysPath.Stat(); err == nil {
		return rootedAuthorizedKeysPath, nil
	}
	if !errors.Is(err, iofs.ErrNotExist) {
		return fs.RootedPath{}, err
	}

	return fs.RootedPath{}, fmt.Errorf("unable to find any authorized keys file")
}
