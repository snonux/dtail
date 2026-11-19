package clients

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
)

// builderKeepAliveInterval is how often the query builder sends a
// keepalive over each session it holds (keepSessionsAlive): well below
// dserver's default IdleSessionTimeoutS of 900 s. dserver closes a
// connection at the deadline it set at its last refresh tick that saw
// activity, timeout + 2 intervals after that tick (ticks every interval =
// timeout/4, at least 1 s, at most 30 s; server.activityConn), so a
// keepalive must be seen by a tick before that deadline: with one every
// 60 s that always holds for a timeout above 48 s (60 s < timeout +
// interval), never for one of 40 s or less (60 s >= timeout + 2 intervals),
// and in between it depends on where the ticks fall. A session closed all
// the same is replaced (holdSessions).
const builderKeepAliveInterval = 60 * time.Second

// keepAliver is implemented by a connection that can send a keepalive the
// server takes as activity (connectors.ServerConnection.KeepAlive). A
// serverless connection has no idle timeout and does not implement it.
type keepAliver interface {
	KeepAlive() error
}

// The connection to a dserver must be one: keepSessionsAlive finds the
// connections to keep alive by this interface, so a changed method would
// otherwise silently end the keepalives.
var _ keepAliver = (*connectors.ServerConnection)(nil)

// The query builder of dmap holds sessions only for its prober, which asks
// at most probeMaxConnections servers (the fields of a log are usually the
// same on every server). So it holds no more sessions than that, however
// many servers the query will run on (holdBuilderSessions): with a large
// fleet it would otherwise open one SSH session per server only to keep it
// idle, and every server that is down would report its failed connection
// again on each retry. The catalog the builder completes from is the view
// of the servers held, as in an interactive dtail it is the view of the
// servers probed. The run connects to every server.

// serverRotation hands out the servers of the builder, by their index in
// the client's connections, to the workers that hold sessions: each worker
// takes the next server no other worker holds, in the client's order and
// around, so that a server that fails or ends its session is replaced by
// the next one and tried again only after all the others.
type serverRotation struct {
	mu     sync.Mutex
	inUse  []bool
	used   []bool
	cursor int
}

func newServerRotation(servers int) *serverRotation {
	return &serverRotation{inUse: make([]bool, servers), used: make([]bool, servers)}
}

// next returns the next server no worker holds and whether it was handed
// out before (its connection ended then and must be created again). There
// always is one: the workers are no more than the servers, and each holds
// at most one.
func (r *serverRotation) next() (index int, usedBefore bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for range r.inUse {
		i := r.cursor
		r.cursor = (r.cursor + 1) % len(r.inUse)
		if !r.inUse[i] {
			usedBefore = r.used[i]
			r.inUse[i], r.used[i] = true, true
			return i, usedBefore
		}
	}
	panic("clients: more query builder session workers than servers")
}

// release gives server index back to the rotation.
func (r *serverRotation) release(index int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inUse[index] = false
}

// holdBuilderSessions runs the sessions of the query builder until ctx
// ends: at most probeMaxConnections at a time, each by a worker that
// replaces a session that ended with one to the next server
// (holdSessions). It returns the highest status of the workers' last
// sessions, 0 unless one of them failed.
func (c *baseClient) holdBuilderSessions(ctx context.Context) int {
	servers := len(c.snapshotConnections())
	rotation := newServerRotation(servers)
	statuses := make([]int, min(servers, probeMaxConnections))
	var wg sync.WaitGroup
	for worker := range statuses {
		wg.Go(func() { statuses[worker] = c.holdSessions(ctx, rotation, worker) })
	}
	wg.Wait()
	// As runConnections: never below 0 (a client handler's status is -1
	// until its server reported one).
	return slices.Max(append(statuses, 0))
}

// holdSessions holds one session at a time until ctx ends and returns the
// status of the last one. When a session ends (the server is down, closed
// the idle session or restarted), it waits like a reconnect does (the one
// policy of the client, retryBackoff through awaitReconnect: 2 s doubling
// up to 60 s with jitter, started over after a session that lasted) and
// goes on with the next server of the rotation; with no other server free
// that is the same one again. What differs from startConnection is what
// follows the wait, on purpose: a connection that can not be created again
// ends a reconnecting dtail's connection, while the builder, which has
// other servers to hold a session on, counts it as a failed session and
// goes on (holdSession).
func (c *baseClient) holdSessions(ctx context.Context, rotation *serverRotation, worker int) (status int) {
	backoff := newRetryBackoff(worker)
	for {
		index, usedBefore := rotation.next()
		server := c.snapshotConnections()[index].Server()
		sessionStart := c.now()
		status = c.holdSession(ctx, index, usedBefore)
		rotation.release(index)
		if !c.awaitReconnect(ctx, backoff, server, sessionStart) {
			return status
		}
	}
}

// holdSession runs one session to the server at index of the client's
// connections and returns its status. A server handed out before gets a
// new connection first (recreateConnection), as its previous one ended.
// While the session runs, its connection is one of those the prober asks
// and the keepalive touches (heldConnections).
func (c *baseClient) holdSession(ctx context.Context, index int, usedBefore bool) int {
	conn := c.snapshotConnections()[index]
	if usedBefore {
		server := conn.Server()
		var err error
		if conn, err = c.recreateConnection(index, server); err != nil {
			c.clientLogger().Error(server, "Unable to recreate connection", err)
			return 1
		}
	}
	c.setHeld(conn, true)
	defer c.setHeld(conn, false)
	// A round for the new session: the prober reads the held connections
	// when a round starts, and the one at the builder's start may have
	// found none yet. Its probe waits for the session to come up.
	c.prober.request()
	return c.runSession(ctx, index, conn)
}

// setHeld adds conn to, or removes it from, the connections the query
// builder holds a session on.
func (c *baseClient) setHeld(conn connectors.Connector, held bool) {
	mu := c.stateMu()
	mu.Lock()
	defer mu.Unlock()
	if held {
		c.held = append(c.held, conn)
		return
	}
	c.held = slices.DeleteFunc(c.held, func(other connectors.Connector) bool { return other == conn })
}

// heldConnections returns the connections the query builder holds a
// session on (or is connecting), in the order they were started.
func (c *baseClient) heldConnections() []connectors.Connector {
	mu := c.stateMu()
	mu.RLock()
	defer mu.RUnlock()
	return slices.Clone(c.held)
}

// probeConnections returns the connections the prober may ask: all of the
// client's, or in the query builder those it holds a session on, as the
// others are not connected (a probe of one would wait for a session that
// is not coming until its round timed out).
func (c *baseClient) probeConnections() []connectors.Connector {
	args, _ := c.snapshotConnectionState()
	if isQueryBuilder(args) {
		return c.heldConnections()
	}
	return c.snapshotConnections()
}

// keepSessionsAlive sends a keepalive over every connection of connections
// that takes one (keepAliver), every interval until ctx ends. The query
// builder runs it over the sessions it holds: they run no workload, so
// nothing else is sent over them once the prober is done (it probes only
// while the session has files and, periodically, only while no line is
// being typed), and dserver would close them as idle after its
// IdleSessionTimeoutS while the user still builds the query. A session
// that ends all the same (a timeout too short for the interval, see
// builderKeepAliveInterval, a restart) is replaced (holdSessions).
// Failures are only logged for debugging: the connection reports its own
// end. Each keepalive is sent by a goroutine of its own, as the request is
// written to the connection and a server that stopped reading blocks that
// write: sent in turn, the connections behind it got theirs late or never
// and were closed as idle. A connection whose previous keepalive is still
// being sent gets no other one (sending), so at most one goroutine per
// connection waits; it ends with the connection.
func (c *baseClient) keepSessionsAlive(ctx context.Context, interval time.Duration,
	connections func() []connectors.Connector) {

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var sending sync.Map
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for _, conn := range connections() {
			alive, ok := conn.(keepAliver)
			if !ok {
				continue
			}
			if _, blocked := sending.LoadOrStore(conn, struct{}{}); blocked {
				continue
			}
			go func() {
				defer sending.Delete(conn)
				if err := alive.KeepAlive(); err != nil {
					c.clientLogger().Debug(conn.Server(), "Keepalive failed", err)
				}
			}()
		}
	}
}
