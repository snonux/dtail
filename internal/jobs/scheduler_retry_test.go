package jobs

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
)

// TestSchedulerRetriesAFailedJobOnTheNextRun runs a scheduled job with real
// mapreduce clients. The first run cannot reach its server (connection
// refused): it must leave no outfile and no .query file. The next run reaches
// the data (in-process, serverless) and writes the result, and the run after
// that skips the job because its outfile exists.
func TestSchedulerRetriesAFailedJobOnTheNextRun(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "app.log")
	lines := strings.Repeat("INFO|1002-071143|1|stats.go:56|8|13|7|0.21|471h0m21s|MAPREDUCE:STATS|"+
		"currentConnections=0|lifetimeConnections=1\n", 3)
	if err := os.WriteFile(logFile, []byte(lines), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}
	outfile := filepath.Join(dir, "result.csv")

	cfg := config.RuntimeConfig{
		Server: config.NewDefaultServerConfigForTest(),
		Client: &config.ClientConfig{},
		Common: &config.CommonConfig{HostnameOverride: "test-host"},
	}
	job := config.Scheduled{}
	job.Name = "retried"
	job.Enable = true
	job.TimeRange = [2]int{0, 24}
	job.Servers = []string{closedTCPAddress(t)}
	job.Files = logFile
	job.Query = "from STATS select count($line),max(lifetimeConnections) group by $hostname"
	job.Outfile = outfile
	cfg.Server.Schedule = []config.Scheduled{job}

	s := newScheduler(cfg, jobTestLoggers)
	logger := &exitLogger{}
	s.logger = logger
	runs := 0
	s.newMaprClient = func(args config.Args, mode clients.MaprClientMode) (backgroundClient, error) {
		runs++
		args.KnownHostsPath = filepath.Join(dir, "known_hosts")
		// From the second run on the data is reachable.
		args.Serverless = runs > 1
		return clients.NewMaprClient(args, cfg, mode, jobTestLoggers)
	}

	s.runJobs(context.Background())
	if runs != 1 {
		t.Fatalf("first run created %d clients, want 1", runs)
	}
	for _, path := range []string{outfile, outfile + ".query", outfile + ".tmp", outfile + ".query.tmp"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed run left %s (stat error %v)", path, err)
		}
	}
	wantLog := "Job retried failed and wrote no outfile " + outfile + ", it runs again on the next scheduler run"
	if !slices.Contains(logger.lines, wantLog) {
		t.Fatalf("log = %q, want %q", logger.lines, wantLog)
	}

	s.runJobs(context.Background())
	if runs != 2 {
		t.Fatalf("second run created %d clients in total, want 2", runs)
	}
	assertJobFile(t, outfile, "count($line),max(lifetimeConnections)\n3,1.000000\n")
	assertJobFile(t, outfile+".query", job.Query+" outfile "+outfile)

	s.runJobs(context.Background())
	if runs != 2 {
		t.Fatalf("run after success created %d clients in total, want 2", runs)
	}
}

// closedTCPAddress returns a local address nothing listens on.
func closedTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return address
}

func assertJobFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
