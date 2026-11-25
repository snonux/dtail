package clients

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal/clients/clientlog"
	"github.com/mimecast/dtail/internal/logging"
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
	// Formats interrupt-driven stats output.
	formatter interruptMessageFormatter
	// Controls how long interrupt output remains visible.
	interruptPause time.Duration
	// interruptOut is where the interrupt statistics go, set while an
	// interactive session's line editor runs (setInterruptOutput); unset,
	// they go to stdout.
	interruptOut atomic.Pointer[io.Writer]
	logger       logging.Logger
	// progress measures the read progress the servers report; nil measures
	// none (see setProgress).
	progress *progressMeter
	// progressOut, if set (--progress), gets a PROGRESS line whenever the
	// progress changed and a final one (finishProgress), whatever the quiet
	// mode. progressMu orders those writes, and progressDone ends them.
	// progressWritten is what the last PROGRESS line showed (nil before the
	// first), so that the final line does not repeat it.
	progressOut     io.Writer
	progressMu      sync.Mutex
	progressDone    bool
	progressWritten *progressTotals
	// statsProgress is the progress the last STATS line showed, so that a
	// STATS line is logged when it changed; used by Start's goroutine only.
	statsProgress progressTotals
}

const (
	// progressTick is how often the read progress is measured and, with
	// --progress, a changed one written: as often as servers report it.
	progressTick = time.Second
	// statsTicks is how many progress ticks make one STATS tick (3 s).
	statsTicks = 3
)

// setInterruptOutput makes printStatsDueInterrupt write to w, or to stdout
// again for nil. The interactive control loop sets its line editor's writer
// (the control terminal, the one Ctrl-C was pressed on), which takes the
// line being edited off the screen around the statistics and draws it
// again below them.
func (s *stats) setInterruptOutput(w io.Writer) {
	if w == nil {
		s.interruptOut.Store(nil)
		return
	}
	s.interruptOut.Store(&w)
}

func newTailStats(servers int, formatter interruptMessageFormatter, interruptPause time.Duration,
	logger logging.Logger) *stats {
	if interruptPause <= 0 {
		interruptPause = 3 * time.Second
	}
	return &stats{
		servers:          servers,
		connectionsEstCh: make(chan struct{}, servers),
		connected:        0,
		formatter:        formatter,
		interruptPause:   interruptPause,
		logger:           logging.OrNop(logger),
	}
}

// setProgress makes the stats report the read progress of source: as fields
// of the STATS lines and, with out set (--progress), as PROGRESS lines to out.
func (s *stats) setProgress(source func() progressTotals, out io.Writer) {
	s.progress = newProgressMeter(source)
	s.progressOut = out
}

// Start starts printing client connection stats every time a signal is
// received and, checked every 3 s, when the connection count or the read
// progress the servers report changed. With --progress it writes a PROGRESS
// line every second the progress changed. quiet drops the periodic STATS
// lines, not the PROGRESS lines nor the statistics asked for with Ctrl-C.
func (s *stats) Start(ctx context.Context, throttleCh <-chan struct{},
	statsCh <-chan string, quiet bool) {

	ticker := time.NewTicker(progressTick)
	defer ticker.Stop()

	// The periodic stats are logged by a goroutine of their own, so that this
	// loop never blocks on a paused client logger (an interactive session
	// paused with an empty line, see interactivePause). Blocked here, it no
	// longer received from statsCh, and the interrupt handler then dropped
	// every Ctrl-C as "stats already printed" instead of exiting on the
	// second one.
	logCh := make(chan map[string]any, 1)
	go s.logStats(ctx, logCh)

	var connectedLast int
	for ticks := 1; ; ticks++ {
		var messages []string
		select {
		case message := <-statsCh:
			messages = append(messages, message)
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
		report := s.tickProgress()
		if len(messages) > 0 || ticks%statsTicks == 0 {
			connectedLast = s.tick(logCh, throttleCh, messages, report, connectedLast, quiet)
		}
	}
}

// tickProgress measures the read progress and writes it as a PROGRESS line
// if it changed (with --progress), and returns it.
func (s *stats) tickProgress() progressReport {
	report, changed := s.measureProgress()
	s.writeProgress(report, changed)
	return report
}

// tick handles a STATS tick of Start or messages, the Ctrl-C hint, which
// force the statistics on the terminal; else a STATS line is logged when the
// connection count or the read progress changed since the last one, unless
// quiet. It returns the connection count to compare the next tick with.
func (s *stats) tick(logCh chan map[string]any, throttleCh <-chan struct{},
	messages []string, report progressReport, connectedLast int, quiet bool) int {

	force := len(messages) > 0
	connected := len(s.connectionsEstCh)
	throttle := len(throttleCh)
	newConnections := connected - connectedLast

	progressChanged := report.Reporting > 0 && report.progressTotals != s.statsProgress
	if (connected == connectedLast && !progressChanged || quiet) && !force {
		return connectedLast
	}
	s.statsProgress = report.progressTotals

	data := s.statsData(connected, newConnections, throttle)
	report.addTo(data)
	if force {
		messages = append(messages, "Connection stats: "+protocol.EncodeStatsLine(data))
		s.printStatsDueInterrupt(messages)
	} else {
		queueLatestStats(logCh, data)
	}

	s.mutex.Lock()
	s.connected = connected
	s.mutex.Unlock()
	return connected
}

// measureProgress measures the read progress, if the stats have a source.
func (s *stats) measureProgress() (progressReport, bool) {
	if s.progress == nil {
		return progressReport{}, false
	}
	return s.progress.measure()
}

// writeProgress writes report as a PROGRESS line to progressOut when it
// changed and a server reported progress, unless finishProgress ran.
func (s *stats) writeProgress(report progressReport, changed bool) {
	if s.progressOut == nil || !changed || report.Reporting == 0 {
		return
	}
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	if !s.progressDone {
		s.writeProgressLocked(report)
	}
}

// writeProgressLocked writes report as a PROGRESS line and remembers what it
// showed. The caller holds progressMu.
func (s *stats) writeProgressLocked(report progressReport) {
	_, _ = fmt.Fprintln(s.progressOut, report.line())
	s.progressWritten = &report.progressTotals
}

// finishProgress writes the final PROGRESS line to progressOut once the
// sessions ended, also when no server reported progress (reporting=0/N: the
// servers predate the progress report, or no file was read), and ends the
// PROGRESS lines. A last line that already showed the final numbers is the
// final line and is not written twice (a short read ended with two identical
// lines). It does nothing without --progress.
func (s *stats) finishProgress() {
	if s == nil || s.progressOut == nil {
		return
	}
	report, _ := s.progress.measure()
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	if s.progressDone {
		return
	}
	s.progressDone = true
	if s.progressWritten == nil || *s.progressWritten != report.progressTotals {
		s.writeProgressLocked(report)
	}
}

// logStats logs the stats queued by Start until ctx is done.
func (s *stats) logStats(ctx context.Context, logCh <-chan map[string]any) {
	for {
		select {
		case data := <-logCh:
			clientlog.Mapreduce(s.logger, "STATS", data)
		case <-ctx.Done():
			return
		}
	}
}

// queueLatestStats queues data for logStats without blocking. Stats still
// waiting to be logged (the logger is paused) are replaced, as only the
// latest state is of interest. Start is the only sender, so the loop ends
// after at most one replacement.
func queueLatestStats(logCh chan map[string]any, data map[string]any) {
	for {
		select {
		case logCh <- data:
			return
		default:
		}
		select {
		case <-logCh:
		default:
		}
	}
}

// printStatsDueInterrupt prints the statistics asked for with Ctrl-C to
// stdout, or to the interactive line editor (setInterruptOutput), and keeps
// the client logger paused for interruptPause so that they stay visible.
func (s *stats) printStatsDueInterrupt(messages []string) {
	var out io.Writer = os.Stdout
	if w := s.interruptOut.Load(); w != nil {
		out = *w
	}
	clientlog.Pause(s.logger)
	for i, message := range messages {
		if s.formatter != nil {
			_, _ = fmt.Fprintln(out, s.formatter.FormatInterruptMessage(i, message))
			continue
		}
		_, _ = fmt.Fprintf(out, " %s\n", message)
	}
	time.Sleep(s.interruptPause)
	clientlog.Resume(s.logger)
}

func (s *stats) statsData(connected, newConnections int,
	throttle int) map[string]any {

	percConnected := percentOf(float64(s.servers), float64(connected))

	data := make(map[string]any)
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

func percentOf(total float64, value float64) float64 {
	if total == 0 || total == value {
		return 100
	}
	return value / (total / 100.0)
}
