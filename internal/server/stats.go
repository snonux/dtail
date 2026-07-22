package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
)

// Used to collect and display various server stats.
type stats struct {
	mutex               sync.Mutex
	currentConnections  int
	lifetimeConnections uint64
	maxConnections      int
	// preAuthConnections counts TCP connections that have been accepted but
	// whose SSH handshake has not yet completed. These are counted against
	// maxConnections so that slow or abusive clients cannot create unbounded
	// goroutines during the handshake phase. Once a handshake succeeds the
	// slot is converted to a regular connection (decrementPreAuth +
	// incrementConnections). On failure the slot is simply released.
	preAuthConnections int
}

func newStats(maxConnections int) stats {
	return stats{
		maxConnections: maxConnections,
	}
}

func (s *stats) incrementConnections() {
	defer s.logServerStats()
	s.mutex.Lock()
	s.currentConnections++
	s.lifetimeConnections++
	s.mutex.Unlock()
}

func (s *stats) decrementConnections() {
	defer s.logServerStats()
	s.mutex.Lock()
	s.currentConnections--
	s.mutex.Unlock()
}

// reservePreAuth increments the pre-auth counter immediately after Accept so
// that slow or unauthenticated handshakes are counted against maxConnections.
// It must be paired with exactly one call to releasePreAuth or
// promotePreAuthToConnection.
func (s *stats) reservePreAuth() {
	defer s.logServerStats()
	s.mutex.Lock()
	s.preAuthConnections++
	s.mutex.Unlock()
}

// releasePreAuth decrements the pre-auth counter without converting the slot
// into a full authenticated connection. Call this on every handshake failure
// path to undo the reservePreAuth reservation.
func (s *stats) releasePreAuth() {
	defer s.logServerStats()
	s.mutex.Lock()
	s.preAuthConnections--
	s.mutex.Unlock()
}

// promotePreAuthToConnection atomically converts a pre-auth reservation into a
// full authenticated connection. It decrements preAuthConnections and
// increments both currentConnections and lifetimeConnections under a single
// lock acquisition so there is no instant where neither counter holds the slot.
func (s *stats) promotePreAuthToConnection() {
	defer s.logServerStats()
	s.mutex.Lock()
	s.preAuthConnections--
	s.currentConnections++
	s.lifetimeConnections++
	s.mutex.Unlock()
}

func (s *stats) hasConnections() bool {
	s.mutex.Lock()
	currentConnections := s.currentConnections
	s.mutex.Unlock()

	has := currentConnections > 0
	dlog.Server.Info("stats", "Server with open connections?",
		has, currentConnections)
	return has
}

func (s *stats) logServerStats() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	data := make(map[string]interface{})
	data["currentConnections"] = s.currentConnections
	data["lifetimeConnections"] = s.lifetimeConnections
	data["preAuthConnections"] = s.preAuthConnections
	dlog.Server.Mapreduce("STATS", data)
}

// serverLimitExceeded checks whether accepting another connection would exceed
// maxConnections. Both authenticated connections (currentConnections) and
// in-progress handshakes (preAuthConnections) are counted so that slow or
// unauthenticated clients cannot bypass the limit by keeping many TCP
// connections open during the handshake phase.
func (s *stats) serverLimitExceeded() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Count both authenticated connections and pre-auth handshakes in progress.
	total := s.currentConnections + s.preAuthConnections
	if total >= s.maxConnections {
		return fmt.Errorf("Exceeded max allowed concurrent connections of %d (current=%d, pre-auth=%d)",
			s.maxConnections, s.currentConnections, s.preAuthConnections)
	}
	return nil
}

func (s *stats) start(ctx context.Context) {
	for {
		select {
		case <-time.NewTimer(time.Second * 10).C:
			s.logServerStats()
		case <-ctx.Done():
			return
		}
	}
}

func (s *stats) waitForConnections() {
	for {
		select {
		case <-time.NewTimer(time.Second).C:
			if !s.hasConnections() {
				return
			}
		}
	}
}
