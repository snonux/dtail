package clients

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/protocol"
)

// Used to collect and display various client stats.
type stats struct {
	// Total amount servers to connect to.
	servers int
	// To keep track of what connected and disconnected
	connectionsEstCh chan struct{}
	// Amount of servers connections are established.
	connected int
	// To synchronize concurrent access.
	mutex sync.Mutex
}

func newTailStats(servers int) *stats {
	return &stats{
		servers:          servers,
		connectionsEstCh: make(chan struct{}, servers),
		connected:        0,
	}
}

// Start starts printing client connection stats every time a signal is recieved or
// connection count has changed.
func (s *stats) Start(ctx context.Context, throttleCh <-chan struct{}, statsCh <-chan string, quiet bool) {
	var connectedLast int

	for {
		var force bool
		var messages []string

		select {
		case message := <-statsCh:
			messages = append(messages, message)
			force = true
		case <-time.After(time.Second * 3):
		case <-ctx.Done():
			return
		}

		connected := len(s.connectionsEstCh)
		throttle := len(throttleCh)

		newConnections := connected - connectedLast
		if (connected == connectedLast || quiet) && !force {
			continue
		}

		switch force {
		case true:
			stats := s.statsLine(connected, newConnections, throttle)
			messages = append(messages, fmt.Sprintf("Connection stats: %s", stats))
			s.printStatsDueInterrupt(messages)
		default:
			data := s.statsData(connected, newConnections, throttle)
			dlog.Client.Mapreduce("STATS", data)
		}

		connectedLast = connected
		s.mutex.Lock()
		s.connected = connected
		s.mutex.Unlock()
	}
}

func (s *stats) printStatsDueInterrupt(messages []string) {
	dlog.Client.Pause()
	for i, message := range messages {
		if i > 0 && config.Client.TermColorsEnable {
			fmt.Println(color.PaintStrWithAttr(message,
				config.Client.TermColors.Client.ClientFg,
				config.Client.TermColors.Client.ClientBg,
				config.Client.TermColors.Client.ClientAttr,
			))
			continue
		}
		fmt.Println(fmt.Sprintf(" %s", message))
	}
	time.Sleep(time.Second * time.Duration(config.InterruptTimeoutS))
	dlog.Client.Resume()
}

func (s *stats) statsData(connected, newConnections int, throttle int) map[string]interface{} {
	percConnected := percentOf(float64(s.servers), float64(connected))

	data := make(map[string]interface{})
	data["connected"] = connected
	data["servers"] = s.servers
	data["connected%"] = int(percConnected)
	data["new"] = newConnections
	data["throttle"] = throttle
	data["goroutines"] = runtime.NumGoroutine()
	data["cgocalls"] = runtime.NumCgoCall()
	data["cpu"] = runtime.NumCPU()

	return data
}

func (s *stats) statsLine(connected, newConnections int, throttle int) string {
	sb := strings.Builder{}

	i := 0
	for k, v := range s.statsData(connected, newConnections, throttle) {
		if i > 0 {
			sb.WriteString(protocol.FieldDelimiter)
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(fmt.Sprintf("%v", v))
		i++
	}

	return sb.String()
}

func (s *stats) numConnected() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.connected
}

func percentOf(total float64, value float64) float64 {
	if total == 0 || total == value {
		return 100
	}
	return value / (total / 100.0)
}
