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
	"github.com/mimecast/dtail/internal/constants"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/protocol"
)

// statsTimer is a reusable timer for periodic statistics reporting,
// providing performance optimization by avoiding repeated timer allocations.
var statsTimer = time.NewTimer(constants.StatsTimerDuration)

// stats collects and displays real-time client statistics including connection
// counts, performance metrics, and system resource usage. It provides both
// periodic updates and interrupt-driven reporting for monitoring client operations.
//
// The statistics system tracks:
// - Server connection status and progress
// - Connection throttling and resource usage
// - System metrics like goroutines and CPU usage
// - Connection establishment rates and changes
type stats struct {
	// servers is the total number of servers that the client will attempt to connect to,
	// used as the baseline for calculating connection progress percentages
	servers int
	
	// connectionsEstCh is a buffered channel that tracks connection establishment events,
	// with each successful connection adding a struct{} to monitor progress
	connectionsEstCh chan struct{}
	
	// connected maintains the current count of established server connections,
	// updated periodically and used for progress reporting
	connected int
	
	// mutex provides thread-safe access to the connected counter when accessed
	// concurrently by statistics reporting and connection management goroutines
	mutex sync.Mutex
}

// newTailStats creates a new statistics tracker for the specified number of servers.
// This constructor initializes all necessary channels and counters for monitoring
// client connection progress and performance metrics.
//
// Parameters:
//   servers: The total number of servers that will be connected to
//
// Returns:
//   *stats: Initialized statistics tracker ready for use
//
// The returned stats instance is configured with:
// - A buffered channel sized to track all server connections
// - Zero initial connection count
// - Thread-safe access controls
func newTailStats(servers int) *stats {
	return &stats{
		servers:          servers,
		connectionsEstCh: make(chan struct{}, servers),
		connected:        0,
	}
}

// Start begins the statistics reporting loop, providing periodic updates and
// interrupt-driven statistics display. This method runs in a separate goroutine
// and continues until the context is cancelled.
//
// Parameters:
//   ctx: Context for cancellation control
//   throttleCh: Channel for monitoring connection throttling status
//   statsCh: Channel for receiving interrupt-driven statistics requests
//   quiet: Whether to suppress automatic periodic statistics updates
//
// Operation modes:
// - Automatic: Periodic updates when connection counts change
// - Interrupt-driven: Immediate updates when messages received on statsCh
// - Quiet mode: Only displays statistics when explicitly requested
//
// This method tracks connection progress, system resources, and provides
// real-time feedback on client operation status.
func (s *stats) Start(ctx context.Context, throttleCh <-chan struct{},
	statsCh <-chan string, quiet bool) {

	var connectedLast int
	for {
		var force bool
		var messages []string

		// Reset the reusable timer to reduce allocations - PBO optimization
		if !statsTimer.Stop() {
			// Drain timer channel if it fired
			select {
			case <-statsTimer.C:
			default:
			}
		}
		statsTimer.Reset(constants.StatsTimerDuration)
		
		select {
		case message := <-statsCh:
			messages = append(messages, message)
			force = true
		case <-statsTimer.C:
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

// printStatsDueInterrupt displays statistics messages when triggered by
// interrupt signals (like SIGUSR1). This method temporarily pauses normal
// log output to display clear statistics information.
//
// Parameters:
//   messages: List of messages to display, with the first being uncolored
//
// Display behavior:
// - Pauses normal log output stream
// - Colors subsequent messages if terminal colors are enabled
// - Waits briefly to ensure message visibility
// - Resumes normal log output
//
// This provides a clean way to view current statistics without interfering
// with the continuous log stream output.
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
	time.Sleep(time.Second * time.Duration(constants.InterruptTimeoutSeconds))
	dlog.Client.Resume()
}

// statsData creates a comprehensive statistics data map containing all
// relevant client performance and connection metrics. This data is used
// for both display formatting and MapReduce-style logging.
//
// Parameters:
//   connected: Current number of established connections
//   newConnections: Number of new connections since last update
//   throttle: Current throttling queue length
//
// Returns:
//   map[string]interface{}: Complete statistics data including:
//     - connected: Current connection count
//     - servers: Total number of target servers
//     - connected%: Connection completion percentage
//     - new: New connections in this period
//     - throttle: Current throttling queue length
//     - goroutines: Current Go routine count
//     - cgocalls: Total CGO calls made
//     - cpu: Number of available CPU cores
func (s *stats) statsData(connected, newConnections int,
	throttle int) map[string]interface{} {

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

// statsLine formats statistics data into a single line string suitable
// for display in interrupt mode. The format uses protocol field delimiters
// to separate key=value pairs for consistent formatting.
//
// Parameters:
//   connected: Current number of established connections
//   newConnections: Number of new connections since last update
//   throttle: Current throttling queue length
//
// Returns:
//   string: Formatted statistics line with key=value pairs separated by protocol delimiters
//
// The output format follows the DTail protocol conventions and provides
// a compact, readable summary of all key statistics.
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

// numConnected returns the current number of established connections
// in a thread-safe manner. This method is used by other components
// that need to check connection status.
//
// Returns:
//   int: Current number of established server connections
//
// Thread safety is ensured through mutex locking to prevent
// race conditions when accessing the connection counter.
func (s *stats) numConnected() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.connected
}

// percentOf calculates the percentage of value relative to total,
// handling edge cases like zero totals and complete percentages.
//
// Parameters:
//   total: The total amount (denominator)
//   value: The current value (numerator)
//
// Returns:
//   float64: Percentage value scaled by constants.PercentageMultiplier
//
// Special cases:
// - Returns 100% when total is zero or equals value
// - Returns standard percentage calculation otherwise
// - Uses constants.PercentageMultiplier for consistent scaling
func percentOf(total float64, value float64) float64 {
	if total == 0 || total == value {
		return constants.PercentageMultiplier
	}
	return value / (total / constants.PercentageMultiplier)
}
