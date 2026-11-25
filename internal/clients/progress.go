package clients

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/protocol"
)

// progressRateWindow is how far back the read rate, and with it the ETA, is
// measured: long enough to smooth the 1 s server reports and the 3 s client
// ticks, short enough to follow a read that speeds up or slows down.
const progressRateWindow = 15 * time.Second

// progressTotals is the read progress of all servers of a client: the sum of
// what each reported last (protocol.HiddenProgressPrefix), how many of them
// reported any, and how many servers there are.
type progressTotals struct {
	protocol.Progress
	Reporting int
	Servers   int
}

// progressSample is the bytes read at one instant, for the rate.
type progressSample struct {
	at    time.Time
	bytes int64
}

// progressReport is what the client shows of the read progress: the totals
// with the rate measured over progressRateWindow and the estimated time left.
type progressReport struct {
	progressTotals
	// Rate is in bytes per second; RateKnown is false until two samples at
	// least a second apart were taken.
	Rate      float64
	RateKnown bool
}

// progressMeter measures the read progress of a client. measure is called by
// the stats loop and, once, by stats.finishProgress, hence mu.
type progressMeter struct {
	mu      sync.Mutex
	source  func() progressTotals
	now     func() time.Time
	samples []progressSample
	last    progressTotals
	// measured is set once measure ran, so that the first totals count as
	// a change.
	measured bool
}

func newProgressMeter(source func() progressTotals) *progressMeter {
	return &progressMeter{source: source, now: time.Now}
}

// measure takes the current totals and returns them as a report, and whether
// they changed since the last measure.
func (m *progressMeter) measure() (progressReport, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	totals := m.source()
	changed := !m.measured || totals != m.last
	m.measured = true
	if totals.Bytes < m.last.Bytes || totals.Total < m.last.Total {
		// Fewer bytes or a smaller total: a new workload (an interactive
		// reload) started over, so the rate starts over too.
		m.samples = nil
	}
	m.last = totals
	m.addSample(totals.Bytes)
	report := progressReport{progressTotals: totals}
	report.Rate, report.RateKnown = m.rate()
	return report, changed
}

// addSample records bytes at now and drops the samples older than the rate
// window, but keeps the newest of those as the window's start.
func (m *progressMeter) addSample(bytes int64) {
	now := m.now()
	m.samples = append(m.samples, progressSample{at: now, bytes: bytes})
	cut := 0
	for cut+1 < len(m.samples) && now.Sub(m.samples[cut+1].at) >= progressRateWindow {
		cut++
	}
	m.samples = m.samples[cut:]
}

// rate returns the bytes per second over the samples, if they span a second
// at least.
func (m *progressMeter) rate() (float64, bool) {
	if len(m.samples) < 2 {
		return 0, false
	}
	first, last := m.samples[0], m.samples[len(m.samples)-1]
	elapsed := last.at.Sub(first.at)
	if elapsed < time.Second {
		return 0, false
	}
	return float64(last.bytes-first.bytes) / elapsed.Seconds(), true
}

// progressTotals sums the progress the client's servers reported so far.
func (c *baseClient) progressTotals() progressTotals {
	connections := c.snapshotConnections()
	totals := progressTotals{Servers: len(connections)}
	for _, conn := range connections {
		reporter, ok := conn.Handler().(handlers.ProgressReporter)
		if !ok {
			continue
		}
		progress, ok := reporter.Progress()
		if !ok {
			continue
		}
		totals.Reporting++
		totals.Bytes += progress.Bytes
		totals.Total += progress.Total
		totals.Files += progress.Files
		totals.FilesTotal += progress.FilesTotal
	}
	return totals
}

// percent returns the share of the total read, capped at 100, and 100 for an
// empty total (nothing to read is all read).
func (r progressReport) percent() float64 {
	if r.Total <= 0 {
		return 100
	}
	return min(100, float64(r.Bytes)*100/float64(r.Total))
}

// percentText returns percent with one decimal, or "unknown" while no server
// reported progress: nothing known is not all done.
func (r progressReport) percentText() string {
	if r.Reporting == 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.1f", r.percent())
}

// eta returns the estimated time left, "unknown" while no server reported
// progress, no rate is known or nothing is read any more although bytes are
// left.
func (r progressReport) eta() string {
	left := r.Total - r.Bytes
	switch {
	case r.Reporting == 0:
		return "unknown"
	case left <= 0:
		return "0s"
	case !r.RateKnown || r.Rate <= 0:
		return "unknown"
	}
	return (time.Duration(float64(left) / r.Rate * float64(time.Second))).Round(time.Second).String()
}

// progressField is one key=value field of a progress report.
type progressField struct {
	key   string
	value string
}

// fields returns the report's fields in the order of a PROGRESS line: the
// raw byte counts for programs, then the same for people, the rate, the ETA,
// the files and how many servers reported.
func (r progressReport) fields() []progressField {
	rate := "unknown"
	if r.RateKnown {
		rate = formatBytes(int64(r.Rate)) + "/s"
	}
	return []progressField{
		{"bytes", fmt.Sprint(r.Bytes)},
		{"total", fmt.Sprint(r.Total)},
		{"progress", formatBytes(r.Bytes) + "/" + formatBytes(r.Total)},
		{"progress%", r.percentText()},
		{"rate", rate},
		{"eta", r.eta()},
		{"files", fmt.Sprintf("%d/%d", r.Files, r.FilesTotal)},
		{"reporting", fmt.Sprintf("%d/%d", r.Reporting, r.Servers)},
	}
}

// line returns the report as a PROGRESS line (without a line break):
// PROGRESS|bytes=...|total=...|... in the order of fields.
func (r progressReport) line() string {
	var sb strings.Builder
	sb.WriteString("PROGRESS")
	for _, field := range r.fields() {
		sb.WriteString(protocol.FieldSeparator())
		sb.WriteString(field.key)
		sb.WriteByte('=')
		sb.WriteString(field.value)
	}
	return sb.String()
}

// addTo adds the report's fields to the data of a STATS line, unless no
// server reported progress (a follow read, or servers of before the progress
// report), which would only add noise.
func (r progressReport) addTo(data map[string]any) {
	if r.Reporting == 0 {
		return
	}
	for _, field := range r.fields() {
		data[field.key] = field.value
	}
}

// formatBytes formats n bytes with IEC units: 512B, 1.50KiB, 3.25GiB.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	value := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	i := -1
	for value >= unit && i < len(units)-1 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.2f%s", value, units[i])
}

// progressOutput is where the PROGRESS lines of --progress go: stderr, so
// that stdout holds the data only, or nowhere without --progress. Interactive
// sessions (and dmap's query builder) write none, as stderr would bypass the
// paused output and the line editor; their STATS lines still carry the
// progress fields.
func (c *baseClient) progressOutput() io.Writer {
	if !c.Progress || c.InteractiveQuery || c.QueryBuilder {
		return nil
	}
	return os.Stderr
}
