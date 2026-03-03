package integrationtests

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

const (
	authKeyFastPathLog      = "Authorized by in-memory auth key store"
	dcatExpectedFirstOutput = "1 Sat  2 Oct 13:46:45 EEST 2021"
)

func TestAuthKeyFastReconnectIntegration(t *testing.T) {
	skipIfNotIntegrationTest(t)
	cleanupTmpFiles(t)

	t.Run("RegistrationFastPathAndFallback", testAuthKeyRegistrationFastPathAndFallback)
	t.Run("TTLExpiry", testAuthKeyTTLExpiry)
	t.Run("MaxKeysPerUser", testAuthKeyMaxKeysPerUser)
	t.Run("NoAuthKeyFlag", testNoAuthKeyFlagDisablesFeature)
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
	waitForServerLogs()
	fastPathCountAfterSecond := server.CountLogLinesContaining(authKeyFastPathLog)
	if fastPathCountAfterSecond < 1 {
		t.Fatalf("Expected fast-path hit before TTL expiry, count=%d", fastPathCountAfterSecond)
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

	stdoutCh, stderrCh, cmdErrCh, err := startCommand(ctx, t, "", "../dserver", args...)
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

	time.Sleep(500 * time.Millisecond)
	return &authKeyServer{
		ctx:    ctx,
		cancel: cancel,
		addr:   fmt.Sprintf("localhost:%d", port),
		logs:   logs,
	}
}

func runDCatWithAuthKey(ctx context.Context, t *testing.T, outFile,
	serverAddress, authKeyPath string, noAuthKey bool) (int, error) {
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

	return runCommand(ctx, t, outFile, "../dcat", args...)
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

func createAuthKeyPair(t *testing.T, keyName string) string {
	t.Helper()

	keyPath := filepath.Join(t.TempDir(), keyName)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Unable to generate private key: %v", err)
	}

	privateKeyBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	if err := os.WriteFile(keyPath, privateKeyBytes, 0600); err != nil {
		t.Fatalf("Unable to write private key: %v", err)
	}

	publicKey, err := gossh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("Unable to generate public key: %v", err)
	}
	if err := os.WriteFile(keyPath+".pub", gossh.MarshalAuthorizedKey(publicKey), 0600); err != nil {
		t.Fatalf("Unable to write public key: %v", err)
	}

	return keyPath
}

func waitForServerLogs() {
	time.Sleep(300 * time.Millisecond)
}
