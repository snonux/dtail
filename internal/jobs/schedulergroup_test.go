package jobs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
)

// groupRecorder is a fake client factory that records what the scheduler
// runs. Clients of a job named in together wait until all of those run at
// the same time, so a scheduler that ran them one after another times out.
type groupRecorder struct {
	t        *testing.T
	together map[string]bool

	mu      sync.Mutex
	events  []string
	args    map[string]config.Args
	running int
	joined  chan struct{}
}

func newGroupRecorder(t *testing.T, together ...string) *groupRecorder {
	r := &groupRecorder{t: t, together: map[string]bool{}, args: map[string]config.Args{},
		joined: make(chan struct{})}
	for _, name := range together {
		r.together[name] = true
	}
	return r
}

func (r *groupRecorder) newClient(args config.Args, _ clients.MaprClientMode) (backgroundClient, error) {
	// The job's query, the start of the client's query, is its name here.
	name, _, _ := strings.Cut(args.QueryStr, " ")
	r.mu.Lock()
	r.args[name] = args
	r.mu.Unlock()
	return recordedClient{recorder: r, name: name}, nil
}

type recordedClient struct {
	recorder *groupRecorder
	name     string
}

func (c recordedClient) Start(context.Context, <-chan string) int {
	r := c.recorder
	r.mu.Lock()
	r.events = append(r.events, "start "+c.name)
	if r.together[c.name] {
		r.running++
		if r.running == len(r.together) {
			close(r.joined)
		}
	}
	r.mu.Unlock()
	if r.together[c.name] {
		select {
		case <-r.joined:
		case <-time.After(10 * time.Second):
			r.t.Errorf("job %s did not run together with the other jobs of its group", c.name)
		}
	}
	r.mu.Lock()
	r.events = append(r.events, "end "+c.name)
	r.mu.Unlock()
	return 0
}

func scheduledJob(t *testing.T, name, files, outfile string, servers ...string) config.Scheduled {
	t.Helper()
	job := config.Scheduled{}
	job.Name = name
	job.Enable = true
	job.TimeRange = [2]int{0, 24}
	job.Files = files
	job.Servers = servers
	job.Query = name
	job.Outfile = outfile
	return job
}

func TestSchedulerRunsJobsOnTheSameFilesTogether(t *testing.T) {
	dir := t.TempDir()
	out := func(name string) string { return filepath.Join(dir, name) }
	disabled := scheduledJob(t, "disabled", "/var/log/a.log", out("disabled"))
	disabled.Enable = false
	jobs := []config.Scheduled{
		scheduledJob(t, "a1", "/var/log/a.log", out("a1")),
		scheduledJob(t, "b", "/var/log/b.log", out("b")),
		scheduledJob(t, "a2", "/var/log/a.log", out("a2")),
		disabled,
		scheduledJob(t, "a3", "/var/log/a.log", out("a3")),
		// Same files, but another server: another group.
		scheduledJob(t, "a-remote", "/var/log/a.log", out("a-remote"), "remote:2222"),
		// Same group key and outfile as a1: runs on its own, after a1.
		scheduledJob(t, "a1-again", "/var/log/a.log", out("a1")),
	}
	s := newScheduler(config.RuntimeConfig{Server: &config.ServerConfig{
		SSHBindAddress: "127.0.0.1", MaxConcurrentCats: 8, Schedule: jobs,
	}}, jobTestLoggers)
	recorder := newGroupRecorder(t, "a1", "a2", "a3")
	s.newMaprClient = recorder.newClient

	s.runJobs(context.Background())

	// The a group runs first, its jobs together; the other groups follow one
	// after another in the order of their first job.
	events := recorder.events
	if len(events) != 12 {
		t.Fatalf("events = %q, want 6 jobs run", events)
	}
	groupEvents := append([]string(nil), events[:6]...)
	for _, event := range groupEvents {
		switch event {
		case "start a1", "start a2", "start a3", "end a1", "end a2", "end a3":
		default:
			t.Fatalf("the a group did not run first, alone: %q", events)
		}
	}
	if want := []string{"start b", "end b", "start a-remote", "end a-remote", "start a1-again", "end a1-again"}; !reflect.DeepEqual(events[6:], want) {
		t.Errorf("events after the a group = %q, want %q", events[6:], want)
	}

	share := recorder.args["a1"].ReadShare
	if share.Members != 3 || share.Group == "" {
		t.Fatalf("a1 read share = %+v, want a group of 3", share)
	}
	for _, name := range []string{"a2", "a3"} {
		if got := recorder.args[name].ReadShare; got != share {
			t.Errorf("%s read share = %+v, want %+v", name, got, share)
		}
	}
	for _, name := range []string{"b", "a-remote", "a1-again"} {
		if got := recorder.args[name].ReadShare; !got.IsZero() {
			t.Errorf("single job %s got read share %+v", name, got)
		}
	}
}

func TestSchedulerReadShareIsRandomPerRun(t *testing.T) {
	dir := t.TempDir()
	s := newScheduler(config.RuntimeConfig{Server: &config.ServerConfig{
		SSHBindAddress: "127.0.0.1", MaxConcurrentCats: 8,
		Schedule: []config.Scheduled{
			scheduledJob(t, "x1", "/var/log/x.log", filepath.Join(dir, "x1")),
			scheduledJob(t, "x2", "/var/log/x.log", filepath.Join(dir, "x2")),
		},
	}}, jobTestLoggers)

	var groups []string
	for range 2 {
		recorder := newGroupRecorder(t, "x1", "x2")
		s.newMaprClient = recorder.newClient
		s.runJobs(context.Background())
		groups = append(groups, recorder.args["x1"].ReadShare.Group)
	}
	if groups[0] == "" || groups[0] == groups[1] {
		t.Errorf("read share groups of two runs = %q, want two different IDs", groups)
	}
}

func TestSchedulerGroupSkipsJobsWithAnExistingOutfile(t *testing.T) {
	dir := t.TempDir()
	done := filepath.Join(dir, "done")
	s := newScheduler(config.RuntimeConfig{Server: &config.ServerConfig{
		SSHBindAddress: "127.0.0.1", MaxConcurrentCats: 8,
		Schedule: []config.Scheduled{
			scheduledJob(t, "y1", "/var/log/y.log", filepath.Join(dir, "y1")),
			scheduledJob(t, "y2", "/var/log/y.log", done),
			scheduledJob(t, "y3", "/var/log/y.log", filepath.Join(dir, "y3")),
		},
	}}, jobTestLoggers)
	if err := os.WriteFile(done, []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := newGroupRecorder(t, "y1", "y3")
	s.newMaprClient = recorder.newClient
	s.runJobs(context.Background())

	if _, ran := recorder.args["y2"]; ran {
		t.Error("a job with an existing outfile ran")
	}
	if got := recorder.args["y1"].ReadShare.Members; got != 2 {
		t.Errorf("group members = %d, want 2: the skipped job does not count", got)
	}
}

func TestGroupDueJobsKeepsGroupsWithinTheCatSlots(t *testing.T) {
	tests := []struct {
		name      string
		maxCats   int
		files     string
		jobs      int
		wantSizes []int
	}{
		{"one file, two slots", 2, "/a.log", 5, []int{2, 2, 1}},
		{"one file, enough slots", 8, "/a.log", 5, []int{5}},
		{"two files, four slots", 4, "/a.log,/b.log", 5, []int{2, 2, 1}},
		{"two files, two slots", 2, "/a.log,/b.log", 3, []int{1, 1, 1}},
		{"no slots configured", 0, "/a.log", 2, []int{1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			var schedule []config.Scheduled
			for i := range tt.jobs {
				name := string(rune('a' + i))
				schedule = append(schedule, scheduledJob(t, name, tt.files, filepath.Join(dir, name)))
			}
			s := newScheduler(config.RuntimeConfig{Server: &config.ServerConfig{
				SSHBindAddress: "127.0.0.1", MaxConcurrentCats: tt.maxCats, Schedule: schedule,
			}}, jobTestLoggers)
			var sizes []int
			for _, group := range groupDueJobs(s.dueJobs()) {
				sizes = append(sizes, len(group))
			}
			if !reflect.DeepEqual(sizes, tt.wantSizes) {
				t.Errorf("group sizes = %v, want %v", sizes, tt.wantSizes)
			}
		})
	}
}
