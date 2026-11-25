package clients

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/protocol"
)

// fakeProgressSource returns totals a test sets.
type fakeProgressSource struct {
	totals progressTotals
}

func (f *fakeProgressSource) get() progressTotals { return f.totals }

// newTestMeter returns a meter over source whose clock a test advances.
func newTestMeter(source *fakeProgressSource) (*progressMeter, *time.Time) {
	now := time.Unix(1000, 0)
	meter := newProgressMeter(source.get)
	meter.now = func() time.Time { return now }
	return meter, &now
}

func totalsOf(bytes, total int64, reporting, servers int) progressTotals {
	return progressTotals{
		Progress:  protocol.Progress{Bytes: bytes, Total: total, Files: 1, FilesTotal: 4},
		Reporting: reporting,
		Servers:   servers,
	}
}

func TestProgressMeterRateAndETA(t *testing.T) {
	source := &fakeProgressSource{totals: totalsOf(0, 1000*1024*1024, 2, 2)}
	meter, now := newTestMeter(source)
	report, changed := meter.measure()
	if !changed || report.RateKnown || report.eta() != "unknown" {
		t.Fatalf("first measure: changed=%v report=%+v eta=%s", changed, report, report.eta())
	}

	*now = now.Add(3 * time.Second)
	source.totals = totalsOf(300*1024*1024, 1000*1024*1024, 2, 2)
	report, changed = meter.measure()
	if !changed || !report.RateKnown || report.Rate != 100*1024*1024 {
		t.Fatalf("rate after 3 s = %v (%v), want 100 MiB/s", report.Rate, report.RateKnown)
	}
	if got := report.eta(); got != "7s" {
		t.Fatalf("eta = %s, want 7s", got)
	}
	want := "PROGRESS|bytes=314572800|total=1048576000|progress=300.00MiB/1000.00MiB|progress%=30.0|" +
		"rate=100.00MiB/s|eta=7s|files=1/4|reporting=2/2"
	if got := report.line(); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}

	if _, changed = meter.measure(); changed {
		t.Fatal("unchanged totals reported as changed")
	}
}

func TestProgressMeterRateWindow(t *testing.T) {
	source := &fakeProgressSource{totals: totalsOf(0, 1<<40, 1, 1)}
	meter, now := newTestMeter(source)
	for i := range 10 {
		*now = now.Add(3 * time.Second)
		// 10 MiB/s for the first 15 s, then 1 MiB/s.
		rate := int64(10)
		if i >= 5 {
			rate = 1
		}
		source.totals.Bytes += rate * 3 * 1024 * 1024
		meter.measure()
	}
	report, _ := meter.measure()
	if report.Rate != 1024*1024 {
		t.Fatalf("rate = %v, want the last 15 s only (1 MiB/s)", report.Rate)
	}
}

func TestProgressMeterStartsOverWithANewWorkload(t *testing.T) {
	source := &fakeProgressSource{totals: totalsOf(500, 1000, 1, 1)}
	meter, now := newTestMeter(source)
	meter.measure()
	*now = now.Add(3 * time.Second)
	source.totals = totalsOf(10, 2000, 1, 1)
	if report, _ := meter.measure(); report.RateKnown {
		t.Fatalf("rate %v carried over a new workload", report.Rate)
	}
}

func TestProgressReportEdges(t *testing.T) {
	done := progressReport{progressTotals: totalsOf(1100, 1000, 1, 1)}
	if done.percent() != 100 || done.eta() != "0s" {
		t.Fatalf("over-read: percent %v eta %s", done.percent(), done.eta())
	}
	empty := progressReport{progressTotals: totalsOf(0, 0, 1, 1)}
	if empty.percent() != 100 {
		t.Fatalf("empty total: percent %v", empty.percent())
	}
	stalled := progressReport{progressTotals: totalsOf(10, 100, 1, 1), RateKnown: true}
	if stalled.eta() != "unknown" {
		t.Fatalf("stalled read: eta %s", stalled.eta())
	}
	for n, want := range map[int64]string{0: "0B", 1023: "1023B", 1536: "1.50KiB", 5 << 30: "5.00GiB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %s, want %s", n, got, want)
		}
	}
}

// TestStatsProgressLines checks the PROGRESS lines of the progress ticks
// (only on changes and with a reporting server, also when quiet), the STATS
// fields of a STATS tick (only when the progress changed since the last
// STATS line), and the final line after finishProgress, which ends them.
func TestStatsProgressLines(t *testing.T) {
	source := &fakeProgressSource{totals: progressTotals{Servers: 2}}
	var out bytes.Buffer
	s := newTailStats(2, nil, time.Millisecond, nil)
	s.setProgress(source.get, &out)
	logCh := make(chan map[string]any, 1)

	report := s.tickProgress()
	s.tick(logCh, nil, nil, report, 0, false)
	if out.Len() != 0 || len(logCh) != 0 {
		t.Fatalf("output without any progress: %q, %d STATS", out.String(), len(logCh))
	}
	source.totals = totalsOf(50, 100, 1, 2)
	s.tickProgress()
	report = s.tickProgress()
	if lines := strings.Count(out.String(), "PROGRESS|"); lines != 1 {
		t.Fatalf("PROGRESS lines = %d in %q, want 1", lines, out.String())
	}
	s.tick(logCh, nil, nil, report, 0, true)
	if len(logCh) != 0 {
		t.Fatal("STATS line logged while quiet")
	}

	source.totals = totalsOf(60, 100, 2, 2)
	report = s.tickProgress()
	s.tick(logCh, nil, nil, report, 0, false)
	data := <-logCh
	if data["progress%"] != "60.0" || data["reporting"] != "2/2" || data["bytes"] != "60" {
		t.Fatalf("STATS data lacks the progress fields: %v", data)
	}
	s.tick(logCh, nil, nil, s.tickProgress(), 0, false)
	if len(logCh) != 0 {
		t.Fatal("STATS line logged for an unchanged progress")
	}

	s.finishProgress()
	s.finishProgress()
	source.totals = totalsOf(70, 100, 2, 2)
	s.tickProgress()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], "bytes=60|") {
		t.Fatalf("lines after the final one: %q", lines)
	}
}

// TestFinishProgressWithoutReports checks the final line of servers that
// report nothing.
func TestFinishProgressWithoutReports(t *testing.T) {
	var out bytes.Buffer
	s := newTailStats(3, nil, time.Millisecond, nil)
	s.setProgress((&fakeProgressSource{totals: progressTotals{Servers: 3}}).get, &out)
	s.finishProgress()
	if !strings.Contains(out.String(), "|progress%=unknown|rate=unknown|eta=unknown|files=0/0|reporting=0/3\n") {
		t.Fatalf("final line = %q, want reporting=0/3", out.String())
	}
	var none *stats
	none.finishProgress()
}
