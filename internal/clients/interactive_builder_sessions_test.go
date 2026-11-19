package clients

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/ssh/client"

	gossh "golang.org/x/crypto/ssh"
)

// heldSessionsTest records the sessions a query builder client runs: which
// servers are connected at a time and how often each was connected.
type heldSessionsTest struct {
	mu      sync.Mutex
	running map[string]bool
	starts  map[string]int
	peak    int
	// end ends the running session of a server when closed.
	end map[string]chan struct{}
	// down are the servers whose sessions end at once (a failed dial).
	down map[string]bool
}

// session runs one session to server until ctx ends, the server is ended
// (endSession) or, for a server that is down, at once.
func (h *heldSessionsTest) session(ctx context.Context, server string) {
	h.mu.Lock()
	h.starts[server]++
	if h.down[server] {
		h.mu.Unlock()
		return
	}
	h.running[server] = true
	h.peak = max(h.peak, len(h.running))
	end := make(chan struct{})
	h.end[server] = end
	h.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-end:
	}
	h.mu.Lock()
	delete(h.running, server)
	h.mu.Unlock()
}

func (h *heldSessionsTest) endSession(server string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.end[server])
}

// connected returns the servers a session runs to, sorted.
func (h *heldSessionsTest) connected() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	servers := make([]string, 0, len(h.running))
	for server := range h.running {
		servers = append(servers, server)
	}
	slices.Sort(servers)
	return servers
}

func (h *heldSessionsTest) started(server string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts[server]
}

// newHeldSessionsTestClient returns a query builder base client with the
// given number of servers (srv1, srv2, ...), whose sessions h runs, and
// whose reconnect waits take no time.
func newHeldSessionsTestClient(servers int, down ...string) (*baseClient, *heldSessionsTest) {
	h := &heldSessionsTest{running: map[string]bool{}, starts: map[string]int{},
		end: map[string]chan struct{}{}, down: map[string]bool{}}
	for _, server := range down {
		h.down[server] = true
	}
	c := &baseClient{mu: newBaseClientMu(), Args: builderTestArgs("/var/log/app.log"),
		loggers: LoggerDependencies{}.normalized()}
	c.stats = newTailStats(servers, nil, 0, nil)
	newConnection := func(server string) connectors.Connector {
		return &finalTestConnector{
			// Status -1, as that of a client handler whose server
			// reported none.
			retryTestConnector: retryTestConnector{server: server,
				handler: &statusTestHandler{status: -1, outcome: handlers.SessionOutcome{}}},
			run: func(ctx context.Context) { h.session(ctx, server) },
		}
	}
	c.connectionFactory = func(server string, _ []gossh.AuthMethod, _ client.HostKeyCallback,
		_ SessionSpec, _ bool) connectors.Connector {
		return newConnection(server)
	}
	for i := range servers {
		c.connections = append(c.connections, newConnection(fmt.Sprintf("srv%d", i+1)))
	}
	c.sleepFn = func(ctx context.Context, _ time.Duration) bool {
		// Yield, so that a test with servers that are all down does not spin.
		time.Sleep(time.Millisecond)
		return ctx.Err() == nil
	}
	return c, h
}

// eventually polls cond for up to 10 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// holdInBackground runs the builder's sessions in the background and
// returns the function that ends them and waits for the status.
func holdInBackground(t *testing.T, c *baseClient) (stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- c.holdBuilderSessions(ctx) }()
	t.Cleanup(cancel)
	return func() int {
		cancel()
		select {
		case status := <-done:
			return status
		case <-time.After(10 * time.Second):
			t.Fatal("the builder's sessions did not end")
			return -1
		}
	}
}

// TestBuilderHoldsOnlyTheSessionsItProbes: with more servers than the
// prober asks, the builder connects only to the first probeMaxConnections;
// the others are not connected, and the prober is given the held ones
// only. With fewer servers it holds them all.
func TestBuilderHoldsOnlyTheSessionsItProbes(t *testing.T) {
	for _, servers := range []int{1, 2, 3, 10} {
		t.Run(fmt.Sprintf("%d servers", servers), func(t *testing.T) {
			c, h := newHeldSessionsTestClient(servers)
			stop := holdInBackground(t, c)
			want := min(servers, probeMaxConnections)
			eventually(t, "the held sessions", func() bool { return len(h.connected()) == want })
			wantServers := []string{"srv1", "srv2", "srv3"}[:want]
			if got := h.connected(); !slices.Equal(got, wantServers) {
				t.Fatalf("connected servers = %q, want %q", got, wantServers)
			}
			var probed []string
			for _, conn := range c.probeConnections() {
				probed = append(probed, conn.Server())
			}
			slices.Sort(probed)
			if !slices.Equal(probed, wantServers) {
				t.Fatalf("connections of the prober = %q, want the held %q", probed, wantServers)
			}
			if status := stop(); status != 0 {
				t.Fatalf("status = %d, want 0", status)
			}
			if h.peak > probeMaxConnections || h.started("srv4") != 0 {
				t.Fatalf("peak sessions %d, srv4 started %d times", h.peak, h.started("srv4"))
			}
			if len(c.heldConnections()) != 0 {
				t.Fatalf("sessions still held after the end: %d", len(c.heldConnections()))
			}
		})
	}
}

// TestBuilderReplacesAnEndedSessionWithTheNextServer: a held session that
// ends (the server closed it, or went down) is replaced by one to the next
// server not held, and servers that are down are passed over, each tried
// once per round of the rotation instead of being retried in place.
func TestBuilderReplacesAnEndedSessionWithTheNextServer(t *testing.T) {
	c, h := newHeldSessionsTestClient(6, "srv2", "srv5")
	stop := holdInBackground(t, c)
	// srv2 is down: the first three sessions are srv1, srv3 and srv4.
	eventually(t, "the first held sessions", func() bool {
		return slices.Equal(h.connected(), []string{"srv1", "srv3", "srv4"})
	})
	h.endSession("srv3")
	// srv5 is down too: srv6 replaces srv3.
	eventually(t, "the replacement", func() bool {
		return slices.Equal(h.connected(), []string{"srv1", "srv4", "srv6"})
	})
	if h.started("srv2") != 1 || h.started("srv5") != 1 {
		t.Fatalf("servers that are down were tried %d and %d times, want once each so far",
			h.started("srv2"), h.started("srv5"))
	}
	stop()
	if h.peak > probeMaxConnections {
		t.Fatalf("peak sessions = %d, want at most %d", h.peak, probeMaxConnections)
	}
}

// TestBuilderWithOneServerReconnectsToIt: with no other server to go to,
// the ended session's server is connected again, on a new connection.
func TestBuilderWithOneServerReconnectsToIt(t *testing.T) {
	c, h := newHeldSessionsTestClient(1)
	stop := holdInBackground(t, c)
	eventually(t, "the session", func() bool { return len(h.connected()) == 1 })
	first := c.snapshotConnections()[0]
	h.endSession("srv1")
	eventually(t, "the reconnect", func() bool { return h.started("srv1") == 2 && len(h.connected()) == 1 })
	if c.snapshotConnections()[0] == first {
		t.Fatal("the ended connection was started again instead of a new one")
	}
	stop()
}

// TestServerRotation covers the order servers are handed out in: in the
// client's order and around, skipping those held, and telling whether a
// server was handed out before.
func TestServerRotation(t *testing.T) {
	r := newServerRotation(3)
	for want := range 3 {
		if index, usedBefore := r.next(); index != want || usedBefore {
			t.Fatalf("next = %d (used before %v), want %d unused", index, usedBefore, want)
		}
	}
	r.release(1)
	if index, usedBefore := r.next(); index != 1 || !usedBefore {
		t.Fatalf("next = %d (used before %v), want the released 1, used before", index, usedBefore)
	}
	r.release(0)
	r.release(2)
	// The cursor is behind 1: 2 comes before 0.
	if index, _ := r.next(); index != 2 {
		t.Fatalf("next = %d, want 2", index)
	}
}

// keepAliveTestConnector counts the keepalives sent over it.
type keepAliveTestConnector struct {
	retryTestConnector
	sent atomic.Int32
	err  error
}

func (c *keepAliveTestConnector) KeepAlive() error {
	c.sent.Add(1)
	return c.err
}

// TestKeepSessionsAlive: every interval a keepalive goes over each
// connection that takes one, also after one failed, the connections are
// read afresh each time (a replaced one is no longer touched), a
// connection without keepalives is passed over, and the loop ends with
// its context.
func TestKeepSessionsAlive(t *testing.T) {
	c := &baseClient{mu: newBaseClientMu(), loggers: LoggerDependencies{}.normalized()}
	first := &keepAliveTestConnector{retryTestConnector: retryTestConnector{server: "srv1"}}
	failing := &keepAliveTestConnector{retryTestConnector: retryTestConnector{server: "srv2"},
		err: errors.New("connection lost")}
	plain := &retryTestConnector{server: "serverless"}
	var mu sync.Mutex
	connections := []connectors.Connector{first, failing, plain}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.keepSessionsAlive(ctx, 5*time.Millisecond, func() []connectors.Connector {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(connections)
		})
	}()
	eventually(t, "repeated keepalives", func() bool { return first.sent.Load() >= 3 && failing.sent.Load() >= 3 })

	replacement := &keepAliveTestConnector{retryTestConnector: retryTestConnector{server: "srv3"}}
	mu.Lock()
	connections = []connectors.Connector{replacement}
	mu.Unlock()
	eventually(t, "keepalives over the replacement", func() bool { return replacement.sent.Load() >= 2 })
	sent := first.sent.Load()
	time.Sleep(30 * time.Millisecond)
	if got := first.sent.Load(); got > sent+1 {
		t.Fatalf("a connection no longer held got %d more keepalives", got-sent)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the keepalive loop did not end with its context")
	}
}
