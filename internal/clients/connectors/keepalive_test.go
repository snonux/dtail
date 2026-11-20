package connectors

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// serveCountingKeepAlives runs an SSH server on each connection that
// counts the keepalive requests it reads and otherwise discards requests,
// as dserver does (gossh.DiscardRequests).
func serveCountingKeepAlives(t *testing.T, keepAlives *atomic.Int32) func(net.Conn) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	return func(conn net.Conn) {
		_, chans, reqs, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		go func() {
			for newChannel := range chans {
				_ = newChannel.Reject(ssh.Prohibited, "test")
			}
		}()
		for req := range reqs {
			if req.Type == KeepAliveRequest {
				keepAlives.Add(1)
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// TestKeepAliveSendsARequestTheServerReads: over a connection that is up,
// KeepAlive sends an SSH request the server reads (which dserver's rolling
// idle timeout takes as activity) and does not wait for an answer; before
// the connection is up and after it ended it sends nothing and is no
// error.
func TestKeepAliveSendsARequestTheServerReads(t *testing.T) {
	var keepAlives atomic.Int32
	address, port := listen(t, serveCountingKeepAlives(t, &keepAlives))
	c := newHandshakeTestConnection(t, address, port, funcHostKeyCallback{check: acceptHostKey}, nil)

	if err := c.KeepAlive(); err != nil {
		t.Fatalf("KeepAlive before the connection is up: %v", err)
	}
	client, err := handshakeWithin(t, c, address, 10*time.Second)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	c.setClient(client)
	for range 3 {
		if err := c.KeepAlive(); err != nil {
			t.Fatalf("KeepAlive: %v", err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for keepAlives.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the server read %d keepalive requests, want 3", keepAlives.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	c.setClient(nil)
	if err := c.KeepAlive(); err != nil {
		t.Fatalf("KeepAlive after the connection ended: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := keepAlives.Load(); got != 3 {
		t.Fatalf("the server read %d keepalive requests, want no more than 3", got)
	}
}

// TestKeepAliveReportsALostConnection: a keepalive over a connection that
// was closed is an error, which the caller only logs.
func TestKeepAliveReportsALostConnection(t *testing.T) {
	var keepAlives atomic.Int32
	address, port := listen(t, serveCountingKeepAlives(t, &keepAlives))
	c := newHandshakeTestConnection(t, address, port, funcHostKeyCallback{check: acceptHostKey}, nil)
	client, err := handshakeWithin(t, c, address, 10*time.Second)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	c.setClient(client)
	_ = client.Close()
	if err := c.KeepAlive(); err == nil {
		t.Fatal("KeepAlive over a closed connection reported no error")
	}
}
