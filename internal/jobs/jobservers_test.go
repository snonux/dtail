package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
)

const testBindAddress = "127.0.0.1"

// TestJobServersStr checks that job Servers are trimmed and empty entries
// dropped before they are joined, and that a list with no server left means
// this dserver (its SSHBindAddress), as doc/server-configuration.md says.
func TestJobServersStr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		servers []string
		want    string
	}{
		{name: "nil servers", servers: nil, want: testBindAddress},
		{name: "empty entry", servers: []string{""}, want: testBindAddress},
		{name: "only blank entries", servers: []string{" ", "\t", ""}, want: testBindAddress},
		{name: "trimmed entries", servers: []string{" alpha:2222 ", "beta\t"}, want: "alpha:2222,beta"},
		{name: "blank entries dropped", servers: []string{"alpha", " ", "", "beta"}, want: "alpha,beta"},
		{name: "server list file", servers: []string{" servers.txt "}, want: "servers.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := jobServersStr(tt.servers, testBindAddress); got != tt.want {
				t.Fatalf("jobServersStr(%q) = %q, want %q", tt.servers, got, tt.want)
			}
		})
	}
}

// TestJobsWithBlankServersRunOnThisDServer checks that a scheduled and a
// continuous job whose Servers are only blank entries connect to this
// dserver instead of failing on the server value " , ".
func TestJobsWithBlankServersRunOnThisDServer(t *testing.T) {
	t.Parallel()

	cfg := config.RuntimeConfig{Server: &config.ServerConfig{SSHBindAddress: testBindAddress}}
	blank := []string{" ", ""}

	s := newScheduler(cfg, jobTestLoggers)
	var scheduledArgs config.Args
	s.newMaprClient = func(args config.Args, _ clients.MaprClientMode) (backgroundClient, error) {
		scheduledArgs = args
		return immediateBackgroundClient{}, nil
	}
	scheduled := config.Scheduled{}
	scheduled.Name = "scheduled-blank-servers"
	scheduled.Query = "select count(*)"
	scheduled.Outfile = filepath.Join(t.TempDir(), "result")
	scheduled.Servers = blank
	runSingleJob(s, &scheduled)
	if scheduledArgs.ServersStr != testBindAddress {
		t.Fatalf("scheduled ServersStr = %q, want %q", scheduledArgs.ServersStr, testBindAddress)
	}

	c := newContinuous(cfg, jobTestLoggers)
	var continuousArgs config.Args
	c.newMaprClient = func(args config.Args, _ clients.MaprClientMode) (backgroundClient, error) {
		continuousArgs = args
		return immediateBackgroundClient{}, nil
	}
	continuous := config.Continuous{}
	continuous.Name = "continuous-blank-servers"
	continuous.Query = "select count(*)"
	continuous.Servers = blank
	c.runJob(context.Background(), &continuous)
	if continuousArgs.ServersStr != testBindAddress {
		t.Fatalf("continuous ServersStr = %q, want %q", continuousArgs.ServersStr, testBindAddress)
	}
}

// TestDynamicServerListIgnoresWhitespaceAroundTheFilePath checks that the
// scheduler, like discovery, treats "servers.txt " as the server list file
// servers.txt, whose servers it must discover again on every wave.
func TestDynamicServerListIgnoresWhitespaceAroundTheFilePath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "servers.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o600); err != nil {
		t.Fatalf("write server list file: %v", err)
	}
	for _, servers := range []string{path, path + " ", " \t" + path + "\t"} {
		if !dynamicServerList(config.Args{ServersStr: servers}) {
			t.Fatalf("dynamicServerList(%q) = false, want true", servers)
		}
	}
	if dynamicServerList(config.Args{ServersStr: "alpha, beta"}) {
		t.Fatal("dynamicServerList of a comma list = true, want false")
	}
}

// TestSchedulerBacksOffAJobWithNoServer runs a scheduled job with a real
// mapreduce client whose server list file names no server (only comment and
// blank lines). Creating the client fails with "no server to connect to",
// which must be a failed run like any other: no outfile, the failure logged
// and the job backed off, so the scheduler does not mistake it for success.
func TestSchedulerBacksOffAJobWithNoServer(t *testing.T) {
	dir := t.TempDir()
	serverFile := filepath.Join(dir, "servers.txt")
	if err := os.WriteFile(serverFile, []byte("# no servers yet\n\n"), 0o600); err != nil {
		t.Fatalf("write server list file: %v", err)
	}
	outfile := filepath.Join(dir, "result.csv")

	cfg := config.RuntimeConfig{
		Server: config.NewDefaultServerConfigForTest(),
		Client: &config.ClientConfig{},
		Common: &config.CommonConfig{HostnameOverride: "test-host"},
	}
	job := config.Scheduled{}
	job.Name = "noserver"
	job.Enable = true
	job.TimeRange = [2]int{0, 24}
	job.Servers = []string{serverFile}
	job.Files = filepath.Join(dir, "app.log")
	job.Query = "from STATS select count($line) group by $hostname"
	job.Outfile = outfile
	cfg.Server.Schedule = []config.Scheduled{job}

	s := newScheduler(cfg, jobTestLoggers)
	logger := &exitLogger{}
	s.logger = logger
	now := time.Date(2026, 9, 22, 1, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	var createErr error
	s.newMaprClient = func(args config.Args, mode clients.MaprClientMode) (backgroundClient, error) {
		args.KnownHostsPath = filepath.Join(dir, "known_hosts")
		args.TrustAllHosts = true
		client, err := clients.NewMaprClient(args, cfg, mode, jobTestLoggers)
		if err != nil {
			createErr = err
			return nil, err
		}
		return client, nil
	}

	s.runJobs(context.Background())
	if createErr == nil || !strings.Contains(createErr.Error(), "no server to connect to") {
		t.Fatalf("client creation error = %v, want no server to connect to", createErr)
	}
	if _, err := os.Stat(outfile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed run left %s (stat error %v)", outfile, err)
	}
	wantLog := "Job noserver failed and wrote no outfile " + outfile + " (failure 1 in a row), it runs again " +
		"from about 2026-09-22 01:01:00"
	if !slices.Contains(logger.lines, wantLog) {
		t.Fatalf("log = %q, want %q", logger.lines, wantLog)
	}
}
