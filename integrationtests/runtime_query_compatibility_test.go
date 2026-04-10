package integrationtests

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/user"

	gossh "golang.org/x/crypto/ssh"
)

func TestDServerProtocolVersionMismatchReportsCompatibilityError(t *testing.T) {
	skipIfNotIntegrationTest(t)

	testLogger := NewTestLogger("TestDServerProtocolVersionMismatchReportsCompatibilityError")
	defer testLogger.WriteLogFile()
	cleanupTmpFiles(t)

	ctx, cancel := createTestContextWithTimeout(t)
	ctx = WithTestLogger(ctx, testLogger)
	defer cancel()

	port := getUniquePortNumber()
	serverStdout, serverStderr, _, err := startCommand(ctx, t, "", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "error",
		"--bindAddress", "localhost",
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		t.Fatalf("start dserver: %v", err)
	}
	_ = startProcessOutputCollector(ctx, serverStdout, serverStderr)
	if err := waitForServerReady(ctx, "localhost", port); err != nil {
		t.Fatalf("wait for dserver: %v", err)
	}

	client, session, stdin, lines, err := openSSHSession(ctx, t, fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("open ssh session: %v", err)
	}
	defer client.Close()
	defer session.Close()

	rawCommand := "protocol 3 base64 " + base64.StdEncoding.EncodeToString([]byte("tail: . /tmp/ignored .")) + ";"
	if _, err := io.WriteString(stdin, rawCommand); err != nil {
		t.Fatalf("write protocol mismatch command: %v", err)
	}

	output, ok := waitForSSHOutputContains(ctx, session, lines,
		"the DTail server protocol version '"+protocol.ProtocolCompat+"' does not match")
	if !ok {
		t.Fatalf("expected protocol mismatch error in SSH output:\n%s", output)
	}
	if !strings.Contains(output, "please update DTail server") {
		t.Fatalf("expected mixed-version compatibility guidance in output:\n%s", output)
	}
}

func openSSHSession(ctx context.Context, t *testing.T, address string) (*gossh.Client, *gossh.Session, io.WriteCloser, <-chan string, error) {
	t.Helper()

	signer, err := loadTestSigner()
	if err != nil {
		return nil, nil, nil, nil, err
	}

	clientConfig := &gossh.ClientConfig{
		User:            user.Name(),
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	conn, err := gossh.Dial("tcp", address, clientConfig)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	session, err := conn.NewSession()
	if err != nil {
		conn.Close()
		return nil, nil, nil, nil, err
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		conn.Close()
		return nil, nil, nil, nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		conn.Close()
		return nil, nil, nil, nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		conn.Close()
		return nil, nil, nil, nil, err
	}
	if err := session.Shell(); err != nil {
		session.Close()
		conn.Close()
		return nil, nil, nil, nil, err
	}

	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	return conn, session, stdin, lines, nil
}

func loadTestSigner() (gossh.Signer, error) {
	for _, path := range []string{"id_rsa", "../id_rsa"} {
		keyBytes, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		signer, err := gossh.ParsePrivateKey(keyBytes)
		if err == nil {
			return signer, nil
		}
	}

	return nil, fmt.Errorf("unable to load test ssh private key from id_rsa")
}

func waitForSSHOutputContains(ctx context.Context, session *gossh.Session, lines <-chan string, needle string) (string, bool) {
	var output strings.Builder
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return output.String(), strings.Contains(output.String(), needle)
			}
			if output.Len() > 0 {
				output.WriteByte('\n')
			}
			output.WriteString(line)
			if strings.Contains(output.String(), needle) {
				_ = session.Close()
				return output.String(), true
			}
		case <-timeout.C:
			_ = session.Close()
			return output.String(), false
		case <-ctx.Done():
			_ = session.Close()
			return output.String(), false
		}
	}
}
