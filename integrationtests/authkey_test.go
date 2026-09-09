package integrationtests

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
	gossh "golang.org/x/crypto/ssh"
)

const (
	authKeyFastPathLog      = "Authorized by in-memory auth key store"
	dcatExpectedFirstOutput = "1 Sat  2 Oct 13:46:45 EEST 2021"
)

type ownedAuthKeyFile struct {
	path string
	info os.FileInfo
}

type authKeyPairOwnership struct {
	privateKey *ownedAuthKeyFile
	publicKey  *ownedAuthKeyFile
}

func TestMain(m *testing.M) {
	var suiteAuthKeyPair authKeyPairOwnership
	if config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		var err error
		suiteAuthKeyPair, err = ensureSuiteAuthKeyPair()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Unable to prepare integration SSH key pair: %v\n", err)
			if cleanupErr := suiteAuthKeyPair.cleanup(); cleanupErr != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Unable to remove incomplete generated integration SSH key pair: %v\n", cleanupErr)
			}
			os.Exit(1)
		}
	}

	exitCode := m.Run()
	if err := suiteAuthKeyPair.cleanup(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Unable to remove generated integration SSH key pair: %v\n", err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

func TestAuthKeyFastReconnectIntegration(t *testing.T) {
	skipIfNotIntegrationTest(t)
	cleanupTmpFiles(t)

	t.Run("RegistrationFastPathAndFallback", testAuthKeyRegistrationFastPathAndFallback)
	t.Run("TTLExpiry", testAuthKeyTTLExpiry)
	t.Run("MaxKeysPerUser", testAuthKeyMaxKeysPerUser)
	t.Run("NoAuthKeyFlag", testNoAuthKeyFlagDisablesFeature)
	t.Run("PassphraseProtectedKey", testPassphraseKeyAuthKeyRegistrationAndFastReconnect)
}

func TestEnsureAuthKeyPair(t *testing.T) {
	writePair := func(keyPath string) (authKeyPairOwnership, error) {
		return writeAuthKeyPairFiles(keyPath, []byte("private key"), []byte("public key"))
	}

	t.Run("Absent", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "id_rsa")
		ownership, err := ensureAuthKeyPair(keyPath, writePair)
		if err != nil {
			t.Fatalf("Unable to ensure absent key pair: %v", err)
		}
		if ownership.privateKey == nil || ownership.publicKey == nil {
			t.Fatalf("Expected ownership of both generated files, got %#v", ownership)
		}
		assertAuthKeyFile(t, keyPath, "private key")
		assertAuthKeyFile(t, keyPath+".pub", "public key")
		if err := ownership.cleanup(); err != nil {
			t.Fatalf("Unable to clean up generated pair: %v", err)
		}
		assertAuthKeyPathMissing(t, keyPath)
		assertAuthKeyPathMissing(t, keyPath+".pub")
	})

	t.Run("Existing", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "id_rsa")
		writeTestFile(t, keyPath, "existing private")
		writeTestFile(t, keyPath+".pub", "existing public")
		writerCalled := false

		ownership, err := ensureAuthKeyPair(keyPath, func(string) (authKeyPairOwnership, error) {
			writerCalled = true
			return authKeyPairOwnership{}, errors.New("unexpected writer call")
		})
		if err != nil {
			t.Fatalf("Unable to preserve existing key pair: %v", err)
		}
		if writerCalled {
			t.Fatal("Expected existing key pair to bypass generation")
		}
		if ownership.privateKey != nil || ownership.publicKey != nil {
			t.Fatalf("Expected no ownership of existing files, got %#v", ownership)
		}
		assertAuthKeyFile(t, keyPath, "existing private")
		assertAuthKeyFile(t, keyPath+".pub", "existing public")
	})

	for _, testCase := range []struct {
		name         string
		existingFile string
	}{
		{name: "PartialPrivate", existingFile: "private"},
		{name: "PartialPublic", existingFile: "public"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			keyPath := filepath.Join(t.TempDir(), "id_rsa")
			existingPath := keyPath
			if testCase.existingFile == "public" {
				existingPath += ".pub"
			}
			writeTestFile(t, existingPath, "existing")

			ownership, err := ensureAuthKeyPair(keyPath, writePair)
			if err == nil || !strings.Contains(err.Error(), "incomplete integration SSH key pair") {
				t.Fatalf("Expected incomplete-pair error, got ownership=%#v err=%v", ownership, err)
			}
			if ownership.privateKey != nil || ownership.publicKey != nil {
				t.Fatalf("Expected no ownership of partial existing pair, got %#v", ownership)
			}
			assertAuthKeyFile(t, existingPath, "existing")
		})
	}
}

func TestEnsureAuthKeyPairRejectsDanglingSymlinks(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		linkedFile string
	}{
		{name: "Private", linkedFile: "private"},
		{name: "Public", linkedFile: "public"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			keyPath := filepath.Join(dir, "id_rsa")
			linkPath := keyPath
			if testCase.linkedFile == "public" {
				linkPath += ".pub"
			}
			targetPath := filepath.Join(dir, "outside-key")
			if err := os.Symlink(targetPath, linkPath); err != nil {
				t.Fatalf("Unable to create dangling symlink: %v", err)
			}

			ownership, err := ensureAuthKeyPair(keyPath, func(keyPath string) (authKeyPairOwnership, error) {
				return writeAuthKeyPairFiles(keyPath, []byte("private"), []byte("public"))
			})
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("Expected symlink rejection, got ownership=%#v err=%v", ownership, err)
			}
			if ownership.privateKey != nil || ownership.publicKey != nil {
				t.Fatalf("Expected no ownership after symlink rejection, got %#v", ownership)
			}
			info, lstatErr := os.Lstat(linkPath)
			if lstatErr != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("Expected dangling symlink to remain, info=%v err=%v", info, lstatErr)
			}
			assertAuthKeyPathMissing(t, targetPath)
		})
	}
}

func TestEnsureAuthKeyPairRejectsNonRegularEntries(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		directoryFile string
	}{
		{name: "Private", directoryFile: "private"},
		{name: "Public", directoryFile: "public"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			keyPath := filepath.Join(t.TempDir(), "id_rsa")
			directoryPath := keyPath
			if testCase.directoryFile == "public" {
				directoryPath += ".pub"
			}
			if err := os.Mkdir(directoryPath, 0700); err != nil {
				t.Fatalf("Unable to create directory at key path: %v", err)
			}

			ownership, err := ensureAuthKeyPair(keyPath, func(keyPath string) (authKeyPairOwnership, error) {
				return writeAuthKeyPairFiles(keyPath, []byte("private"), []byte("public"))
			})
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("Expected non-regular entry rejection, got ownership=%#v err=%v", ownership, err)
			}
			if ownership.privateKey != nil || ownership.publicKey != nil {
				t.Fatalf("Expected no ownership after directory rejection, got %#v", ownership)
			}
			info, lstatErr := os.Lstat(directoryPath)
			if lstatErr != nil || !info.IsDir() {
				t.Fatalf("Expected directory to remain, info=%v err=%v", info, lstatErr)
			}
		})
	}
}

func TestEnsureAuthKeyPairHandlesCreateCollisions(t *testing.T) {
	t.Run("Private", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "id_rsa")
		ownership, err := ensureAuthKeyPair(keyPath, func(keyPath string) (authKeyPairOwnership, error) {
			writeTestFile(t, keyPath, "colliding private")
			return writeAuthKeyPairFiles(keyPath, []byte("generated private"), []byte("generated public"))
		})
		if err == nil || !errors.Is(err, os.ErrExist) {
			t.Fatalf("Expected private-key collision, got ownership=%#v err=%v", ownership, err)
		}
		if ownership.privateKey != nil || ownership.publicKey != nil {
			t.Fatalf("Expected no ownership of colliding private key, got %#v", ownership)
		}
		assertAuthKeyFile(t, keyPath, "colliding private")
		assertAuthKeyPathMissing(t, keyPath+".pub")
	})

	t.Run("Public", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "id_rsa")
		ownership, err := ensureAuthKeyPair(keyPath, func(keyPath string) (authKeyPairOwnership, error) {
			writeTestFile(t, keyPath+".pub", "colliding public")
			return writeAuthKeyPairFiles(keyPath, []byte("generated private"), []byte("generated public"))
		})
		if err == nil || !errors.Is(err, os.ErrExist) {
			t.Fatalf("Expected public-key collision, got ownership=%#v err=%v", ownership, err)
		}
		if ownership.privateKey == nil || ownership.publicKey != nil {
			t.Fatalf("Expected ownership of only generated private key, got %#v", ownership)
		}
		if cleanupErr := ownership.cleanup(); cleanupErr != nil {
			t.Fatalf("Unable to clean up partially generated pair: %v", cleanupErr)
		}
		assertAuthKeyPathMissing(t, keyPath)
		assertAuthKeyFile(t, keyPath+".pub", "colliding public")
	})
}

func TestAuthKeyPairCleanupPreservesReplacement(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_rsa")
	replacementPath := filepath.Join(dir, "replacement")
	writeTestFile(t, replacementPath, "replacement private")

	ownership, err := writeAuthKeyPairFiles(keyPath, []byte("generated private"), []byte("generated public"))
	if err != nil {
		t.Fatalf("Unable to create owned key pair: %v", err)
	}
	if err := os.Rename(replacementPath, keyPath); err != nil {
		t.Fatalf("Unable to replace owned private key: %v", err)
	}

	err = ownership.cleanup()
	if err == nil || !strings.Contains(err.Error(), "no longer refers to the generated file") {
		t.Fatalf("Expected replacement identity error, got %v", err)
	}
	assertAuthKeyFile(t, keyPath, "replacement private")
	assertAuthKeyPathMissing(t, keyPath+".pub")
}

func testAuthKeyRegistrationFastPathAndFallback(t *testing.T) {
	authKeyPath := createAuthKeyPair(t, "authkey-registration")
	server := startAuthKeyServer(t, "")
	defer server.Stop()

	exitCode, err := runDCatWithAuthKey(server.Context(), t, "authkey_registration_1.tmp", server.Address(), authKeyPath, false)
	if err != nil || exitCode != 0 {
		t.Fatalf("Expected first connection to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_registration_1.tmp")
	waitForServerLogs()
	if got := server.CountLogLinesContaining(authKeyFastPathLog); got != 0 {
		t.Fatalf("Expected first connection to use fallback, fast-path count=%d", got)
	}

	exitCode, err = runDCatWithAuthKey(server.Context(), t, "authkey_registration_2.tmp", server.Address(), authKeyPath, false)
	if err != nil || exitCode != 0 {
		t.Fatalf("Expected second connection to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_registration_2.tmp")
	waitForServerLogs()
	if got := server.CountLogLinesContaining(authKeyFastPathLog); got < 1 {
		t.Fatalf("Expected fast-path authorization after registration, fast-path count=%d", got)
	}

	server.Stop()
	time.Sleep(300 * time.Millisecond)

	restartedServer := startAuthKeyServer(t, "")
	defer restartedServer.Stop()

	exitCode, err = runDCatWithAuthKey(restartedServer.Context(), t, "authkey_registration_3.tmp", restartedServer.Address(), authKeyPath, false)
	if err != nil || exitCode != 0 {
		t.Fatalf("Expected fallback after restart to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_registration_3.tmp")
	waitForServerLogs()
	if got := restartedServer.CountLogLinesContaining(authKeyFastPathLog); got != 0 {
		t.Fatalf("Expected no fast-path hit on first post-restart connection, fast-path count=%d", got)
	}
}

func testAuthKeyTTLExpiry(t *testing.T) {
	authKeyPath := createAuthKeyPair(t, "authkey-ttl")
	ttlSeconds := 8
	cfgFile := writeAuthKeyServerConfig(t, ttlSeconds, 5)
	server := startAuthKeyServer(t, cfgFile)
	defer server.Stop()

	exitCode, err := runDCatWithAuthKey(server.Context(), t, "authkey_ttl_1.tmp", server.Address(), authKeyPath, false)
	if err != nil || exitCode != 0 {
		t.Fatalf("Expected first connection to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_ttl_1.tmp")

	exitCode, err = runDCatWithAuthKey(server.Context(), t, "authkey_ttl_2.tmp", server.Address(), authKeyPath, false)
	if err != nil || exitCode != 0 {
		t.Fatalf("Expected second connection to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_ttl_2.tmp")
	fastPathCountAfterSecond := waitForLogCountAtLeast(server, authKeyFastPathLog, 1, 5*time.Second)
	if fastPathCountAfterSecond < 1 {
		t.Fatalf("Expected fast-path hit before TTL expiry, count=%d\nserver logs:\n%s",
			fastPathCountAfterSecond, strings.Join(server.LogLines(), "\n"))
	}

	time.Sleep(time.Duration(ttlSeconds+1) * time.Second)
	exitCode, err = runDCatWithAuthKey(server.Context(), t, "authkey_ttl_3.tmp", server.Address(), authKeyPath, false)
	if err != nil || exitCode != 0 {
		t.Fatalf("Expected fallback after TTL expiry to still connect, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_ttl_3.tmp")
	waitForServerLogs()
	fastPathCountAfterThird := server.CountLogLinesContaining(authKeyFastPathLog)
	if fastPathCountAfterThird != fastPathCountAfterSecond {
		t.Fatalf("Expected TTL-expired key to stop fast-path hits: before=%d after=%d",
			fastPathCountAfterSecond, fastPathCountAfterThird)
	}
}

func testAuthKeyMaxKeysPerUser(t *testing.T) {
	authKeyOne := createAuthKeyPair(t, "authkey-max-one")
	authKeyTwo := createAuthKeyPair(t, "authkey-max-two")
	cfgFile := writeAuthKeyServerConfig(t, 3600, 1)
	server := startAuthKeyServer(t, cfgFile)
	defer server.Stop()

	if exitCode, err := runDCatWithAuthKey(server.Context(), t, "authkey_max_1.tmp", server.Address(), authKeyOne, false); err != nil || exitCode != 0 {
		t.Fatalf("Expected first key registration to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_max_1.tmp")
	if exitCode, err := runDCatWithAuthKey(server.Context(), t, "authkey_max_2.tmp", server.Address(), authKeyTwo, false); err != nil || exitCode != 0 {
		t.Fatalf("Expected second key registration to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_max_2.tmp")
	waitForServerLogs()
	initialFastPathCount := server.CountLogLinesContaining(authKeyFastPathLog)

	if exitCode, err := runDCatWithAuthKey(server.Context(), t, "authkey_max_3.tmp", server.Address(), authKeyOne, false); err != nil || exitCode != 0 {
		t.Fatalf("Expected first key connection (after max eviction) to succeed via fallback, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_max_3.tmp")
	waitForServerLogs()
	afterOldKeyCount := server.CountLogLinesContaining(authKeyFastPathLog)
	if afterOldKeyCount != initialFastPathCount {
		t.Fatalf("Expected evicted old key to avoid fast-path hit: before=%d after=%d",
			initialFastPathCount, afterOldKeyCount)
	}

	if exitCode, err := runDCatWithAuthKey(server.Context(), t, "authkey_max_4.tmp", server.Address(), authKeyOne, false); err != nil || exitCode != 0 {
		t.Fatalf("Expected re-registered first key to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_max_4.tmp")
	waitForServerLogs()
	afterNewKeyCount := server.CountLogLinesContaining(authKeyFastPathLog)
	if afterNewKeyCount <= afterOldKeyCount {
		t.Fatalf("Expected re-registered key to use fast-path: old-count=%d new-count=%d", afterOldKeyCount, afterNewKeyCount)
	}
}

func testNoAuthKeyFlagDisablesFeature(t *testing.T) {
	authKeyPath := createAuthKeyPair(t, "authkey-noauth")
	server := startAuthKeyServer(t, "")
	defer server.Stop()

	if exitCode, err := runDCatWithAuthKey(server.Context(), t, "authkey_noauth_1.tmp", server.Address(), authKeyPath, true); err != nil || exitCode != 0 {
		t.Fatalf("Expected first --no-auth-key connection to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_noauth_1.tmp")
	if exitCode, err := runDCatWithAuthKey(server.Context(), t, "authkey_noauth_2.tmp", server.Address(), authKeyPath, true); err != nil || exitCode != 0 {
		t.Fatalf("Expected second --no-auth-key connection to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_noauth_2.tmp")

	waitForServerLogs()
	if got := server.CountLogLinesContaining(authKeyFastPathLog); got != 0 {
		t.Fatalf("Expected --no-auth-key to prevent fast-path registration, fast-path count=%d", got)
	}
}

func testPassphraseKeyAuthKeyRegistrationAndFastReconnect(t *testing.T) {
	const passphrase = "secret-passphrase"

	authKeyPath := createPassphraseAuthKeyPair(t, "authkey-passphrase", passphrase)
	server := startAuthKeyServer(t, "")
	defer server.Stop()

	env := map[string]string{
		"DTAIL_KEY_PASSPHRASE": passphrase,
	}

	exitCode, err := runDCatWithAuthKeyAndEnv(server.Context(), t,
		"authkey_passphrase_1.tmp", server.Address(), authKeyPath, false, env)
	if err != nil || exitCode != 0 {
		t.Fatalf("Expected first passphrase-protected connection to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_passphrase_1.tmp")
	waitForServerLogs()
	if got := server.CountLogLinesContaining(authKeyFastPathLog); got != 0 {
		t.Fatalf("Expected first passphrase-protected connection to use fallback, fast-path count=%d", got)
	}

	exitCode, err = runDCatWithAuthKeyAndEnv(server.Context(), t,
		"authkey_passphrase_2.tmp", server.Address(), authKeyPath, false, env)
	if err != nil || exitCode != 0 {
		t.Fatalf("Expected second passphrase-protected connection to succeed, exit=%d err=%v", exitCode, err)
	}
	assertDCatSuccessfulOutput(t, "authkey_passphrase_2.tmp")
	fastPathCount := waitForLogCountAtLeast(server, authKeyFastPathLog, 1, 5*time.Second)
	if fastPathCount < 1 {
		t.Fatalf("Expected passphrase-protected key to use fast-path on reconnect, count=%d\nserver logs:\n%s",
			fastPathCount, strings.Join(server.LogLines(), "\n"))
	}
}

type authKeyServer struct {
	ctx    context.Context
	cancel context.CancelFunc
	addr   string
	logs   *authKeyServerLogs
}

func (s *authKeyServer) Stop() {
	s.cancel()
}

func (s *authKeyServer) Context() context.Context {
	return s.ctx
}

func (s *authKeyServer) Address() string {
	return s.addr
}

func (s *authKeyServer) CountLogLinesContaining(substring string) int {
	return s.logs.countContaining(substring)
}

func (s *authKeyServer) LogLines() []string {
	return s.logs.snapshot()
}

type authKeyServerLogs struct {
	mu    sync.Mutex
	lines []string
}

func newAuthKeyServerLogs() *authKeyServerLogs {
	return &authKeyServerLogs{
		lines: make([]string, 0, 128),
	}
}

func (l *authKeyServerLogs) append(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *authKeyServerLogs) countContaining(substring string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	count := 0
	for _, line := range l.lines {
		if strings.Contains(line, substring) {
			count++
		}
	}
	return count
}

func (l *authKeyServerLogs) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	lines := make([]string, len(l.lines))
	copy(lines, l.lines)
	return lines
}

func startAuthKeyServer(t *testing.T, cfgFile string) *authKeyServer {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	port := getUniquePortNumber()
	args := []string{
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "info",
		"--bindAddress", "localhost",
		"--port", fmt.Sprintf("%d", port),
	}
	if cfgFile != "" {
		args = append(args, "--cfg", cfgFile)
	}

	stdoutCh, stderrCh, cmdErrCh, err := startCommandWithEnv(ctx, t, "", "../dserver",
		map[string]string{"DTAIL_TURBOBOOST_DISABLE": "yes"}, args...)
	if err != nil {
		cancel()
		t.Fatalf("Unable to start dserver: %v", err)
	}

	logs := newAuthKeyServerLogs()
	go func() {
		for {
			select {
			case line, ok := <-stdoutCh:
				if ok {
					logs.append(line)
				}
			case line, ok := <-stderrCh:
				if ok {
					logs.append(line)
				}
			case err := <-cmdErrCh:
				if err != nil {
					logs.append(err.Error())
				}
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	if err := waitForServerReady(ctx, "localhost", port); err != nil {
		cancel()
		t.Fatalf("Unable to start dserver: %v", err)
	}
	return &authKeyServer{
		ctx:    ctx,
		cancel: cancel,
		addr:   fmt.Sprintf("localhost:%d", port),
		logs:   logs,
	}
}

func runDCatWithAuthKey(ctx context.Context, t *testing.T, outFile,
	serverAddress, authKeyPath string, noAuthKey bool) (int, error) {
	return runDCatWithAuthKeyAndEnv(ctx, t, outFile, serverAddress, authKeyPath, noAuthKey, nil)
}

func runDCatWithAuthKeyAndEnv(ctx context.Context, t *testing.T, outFile,
	serverAddress, authKeyPath string, noAuthKey bool, env map[string]string) (int, error) {
	t.Helper()

	args := []string{
		"--plain",
		"--cfg", "none",
		"--servers", serverAddress,
		"--files", "dcat1a.txt",
		"--trustAllHosts",
		"--noColor",
		"--auth-key-path", authKeyPath,
	}
	if noAuthKey {
		args = append(args, "--no-auth-key")
	}

	return runCommandWithEnv(ctx, t, outFile, "../dcat", env, args...)
}

func assertDCatSuccessfulOutput(t *testing.T, outFile string) {
	t.Helper()

	outBytes, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("Unable to read dcat output file %s: %v", outFile, err)
	}

	output := string(outBytes)
	if strings.Contains(output, "SSH handshake failed") {
		t.Fatalf("Expected successful SSH connection, got handshake failure in %s:\n%s", outFile, output)
	}
	if !strings.Contains(output, dcatExpectedFirstOutput) {
		t.Fatalf("Expected dcat output to contain %q in %s, got:\n%s", dcatExpectedFirstOutput, outFile, output)
	}
}

func writeAuthKeyServerConfig(t *testing.T, ttlSeconds, maxPerUser int) string {
	t.Helper()

	cfgPath := filepath.Join(t.TempDir(), "authkey_server_config.json")
	cfgContent := fmt.Sprintf(
		`{"Server":{"AuthKeyEnabled":true,"AuthKeyTTLSeconds":%d,"AuthKeyMaxPerUser":%d}}`,
		ttlSeconds, maxPerUser,
	)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("Unable to write auth-key server config: %v", err)
	}
	return cfgPath
}

func ensureSuiteAuthKeyPair() (authKeyPairOwnership, error) {
	const keyPath = "id_rsa"
	return ensureAuthKeyPair(keyPath, generateAuthKeyPair)
}

func ensureAuthKeyPair(keyPath string,
	writePair func(string) (authKeyPairOwnership, error)) (authKeyPairOwnership, error) {
	publicKeyPath := keyPath + ".pub"
	privateKeyExists, err := inspectAuthKeyFile(keyPath)
	if err != nil {
		return authKeyPairOwnership{}, err
	}
	publicKeyExists, err := inspectAuthKeyFile(publicKeyPath)
	if err != nil {
		return authKeyPairOwnership{}, err
	}

	if privateKeyExists != publicKeyExists {
		return authKeyPairOwnership{}, fmt.Errorf("incomplete integration SSH key pair: %s exists=%t, %s exists=%t",
			keyPath, privateKeyExists, publicKeyPath, publicKeyExists)
	}
	if privateKeyExists {
		return authKeyPairOwnership{}, nil
	}

	ownership, err := writePair(keyPath)
	if err != nil {
		return ownership, fmt.Errorf("generate integration SSH key pair: %w", err)
	}
	return ownership, nil
}

func inspectAuthKeyFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("integration SSH key %s is not a regular file (mode %s)", path, info.Mode())
		}
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("inspect integration SSH key %s: %w", path, err)
}

func createAuthKeyPair(t *testing.T, keyName string) string {
	t.Helper()

	keyPath := filepath.Join(t.TempDir(), keyName)
	createAuthKeyPairAtPath(t, keyPath)
	return keyPath
}

func createAuthKeyPairAtPath(t *testing.T, keyPath string) {
	t.Helper()

	ownership, err := generateAuthKeyPair(keyPath)
	if err != nil {
		cleanupErr := ownership.cleanup()
		t.Fatalf("Unable to create auth-key pair: %v", errors.Join(err, cleanupErr))
	}
}

func generateAuthKeyPair(keyPath string) (authKeyPairOwnership, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return authKeyPairOwnership{}, fmt.Errorf("generate private key: %w", err)
	}

	privateKeyBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	return writeAuthKeyPair(keyPath, privateKeyBytes, &privateKey.PublicKey)
}

func createPassphraseAuthKeyPair(t *testing.T, keyName, passphrase string) string {
	t.Helper()

	keyPath := filepath.Join(t.TempDir(), keyName)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Unable to generate private key: %v", err)
	}

	privateKeyBlock, err := gossh.MarshalPrivateKeyWithPassphrase(privateKey, "", []byte(passphrase))
	if err != nil {
		t.Fatalf("Unable to marshal encrypted private key: %v", err)
	}
	ownership, err := writeAuthKeyPair(keyPath, pem.EncodeToMemory(privateKeyBlock), &privateKey.PublicKey)
	if err != nil {
		cleanupErr := ownership.cleanup()
		t.Fatalf("Unable to write auth-key pair: %v", errors.Join(err, cleanupErr))
	}

	return keyPath
}

func writeAuthKeyPair(keyPath string, privateKeyBytes []byte,
	publicKey *rsa.PublicKey) (authKeyPairOwnership, error) {
	sshPublicKey, err := gossh.NewPublicKey(publicKey)
	if err != nil {
		return authKeyPairOwnership{}, fmt.Errorf("generate public key: %w", err)
	}
	return writeAuthKeyPairFiles(keyPath, privateKeyBytes, gossh.MarshalAuthorizedKey(sshPublicKey))
}

func writeAuthKeyPairFiles(keyPath string, privateKeyBytes,
	publicKeyBytes []byte) (authKeyPairOwnership, error) {
	var ownership authKeyPairOwnership
	privateKey, err := writeAuthKeyFile(keyPath, privateKeyBytes)
	if err != nil {
		return ownership, fmt.Errorf("write private key: %w", err)
	}
	ownership.privateKey = privateKey

	publicKey, err := writeAuthKeyFile(keyPath+".pub", publicKeyBytes)
	if err != nil {
		return ownership, fmt.Errorf("write public key: %w", err)
	}
	ownership.publicKey = publicKey
	return ownership, nil
}

func writeAuthKeyFile(path string, contents []byte) (*ownedAuthKeyFile, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}

	info, statErr := file.Stat()
	if statErr != nil {
		closeErr := file.Close()
		return nil, errors.Join(
			fmt.Errorf("inspect created %s: %w", path, statErr),
			wrapAuthKeyCloseError(path, closeErr),
		)
	}
	ownedFile := &ownedAuthKeyFile{path: path, info: info}

	if _, writeErr := file.Write(contents); writeErr != nil {
		closeErr := file.Close()
		return ownedFile, errors.Join(
			fmt.Errorf("write %s: %w", path, writeErr),
			wrapAuthKeyCloseError(path, closeErr),
		)
	}
	if err := file.Close(); err != nil {
		return ownedFile, fmt.Errorf("close %s: %w", path, err)
	}
	return ownedFile, nil
}

func wrapAuthKeyCloseError(path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close %s: %w", path, err)
}

func (o authKeyPairOwnership) cleanup() error {
	return errors.Join(
		removeOwnedAuthKeyFile(o.privateKey),
		removeOwnedAuthKeyFile(o.publicKey),
	)
}

func removeOwnedAuthKeyFile(ownedFile *ownedAuthKeyFile) error {
	if ownedFile == nil {
		return nil
	}

	currentInfo, err := os.Lstat(ownedFile.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect generated %s before removal: %w", ownedFile.path, err)
	}
	if !currentInfo.Mode().IsRegular() || !os.SameFile(ownedFile.info, currentInfo) {
		return fmt.Errorf("refusing to remove %s: path no longer refers to the generated file", ownedFile.path)
	}
	if err := os.Remove(ownedFile.path); err != nil {
		return fmt.Errorf("remove %s: %w", ownedFile.path, err)
	}
	return nil
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("Unable to write test file %s: %v", path, err)
	}
}

func assertAuthKeyFile(t *testing.T, path, expectedContents string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Unable to inspect auth-key file %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("Expected %s to be a regular file, mode=%s", path, info.Mode())
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("Expected %s permissions to exclude group/other access, mode=%s", path, info.Mode())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Unable to read auth-key file %s: %v", path, err)
	}
	if string(contents) != expectedContents {
		t.Fatalf("Unexpected contents for %s: got %q want %q", path, contents, expectedContents)
	}
}

func assertAuthKeyPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("Expected auth-key path %s to be absent, got err=%v", path, err)
	}
}

func waitForServerLogs() {
	time.Sleep(300 * time.Millisecond)
}

func waitForLogCountAtLeast(server *authKeyServer, substring string, minCount int, timeout time.Duration) int {
	if minCount <= 0 {
		return server.CountLogLinesContaining(substring)
	}

	deadline := time.Now().Add(timeout)
	for {
		count := server.CountLogLinesContaining(substring)
		if count >= minCount {
			return count
		}
		if time.Now().After(deadline) {
			return count
		}
		time.Sleep(100 * time.Millisecond)
	}
}
