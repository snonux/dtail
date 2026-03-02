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
	dlog.Server.Mapreduce("STATS", data)
}

func (s *stats) serverLimitExceeded() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.currentConnections >= s.maxConnections {
		return fmt.Errorf("Exceeded max allowed concurrent connections of %d",
			s.maxConnections)
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
