package jobs

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
)

// modelJobs is a fake job client for the scheduler model test. A job reads
// the files its patterns match, then, unless it fails, writes its outfile's
// .query file and its outfile with its name and what it read, as a MapReduce
// job does. So the files a job leaves behind tell which job ran, and what it
// read, which depends on the jobs that ran before it.
type modelJobs struct {
	fails map[string]bool

	mu  sync.Mutex
	ran []string
}

func (m *modelJobs) run(name, files, outfile string) {
	var read []string
	for _, pattern := range strings.Split(files, ",") {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			content, err := os.ReadFile(match)
			if err == nil {
				read = append(read, match+"="+string(content))
			}
		}
	}
	m.mu.Lock()
	m.ran = append(m.ran, name)
	m.mu.Unlock()
	if m.fails[name] {
		return
	}
	result := name + " read [" + strings.Join(read, " ") + "]"
	_ = os.WriteFile(outfile+".query", []byte(name), 0o600)
	_ = os.WriteFile(outfile, []byte(result), 0o600)
}

func (m *modelJobs) newClient(args config.Args, _ clients.MaprClientMode) (backgroundClient, error) {
	// The job's query is its name; the scheduler appends the outfile.
	name, outfile, _ := strings.Cut(args.QueryStr, " outfile ")
	return modelClient{jobs: m, name: name, files: args.What, outfile: outfile}, nil
}

type modelClient struct {
	jobs                 *modelJobs
	name, files, outfile string
}

func (c modelClient) Start(context.Context, <-chan string) int {
	c.jobs.run(c.name, c.files, c.outfile)
	return 0
}

// runSequentially is the scheduler that ran every job on its own, in the
// configured order: a job runs if it is enabled, in its time range and its
// outfile does not exist.
func runSequentially(jobs []config.Scheduled, now time.Time, m *modelJobs) {
	for _, job := range jobs {
		if !job.Enable || now.Hour() < job.TimeRange[0] || now.Hour() >= job.TimeRange[1] {
			continue
		}
		if _, err := os.Stat(job.Outfile); !os.IsNotExist(err) {
			continue
		}
		m.run(job.Query, job.Files, job.Outfile)
	}
}

// dirContents returns every file below the working directory with its
// content.
func dirContents(t *testing.T) map[string]string {
	t.Helper()
	contents := map[string]string{}
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return err
		}
		content, err := os.ReadFile(path)
		contents[path] = string(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func randomJobs(rng *rand.Rand) ([]config.Scheduled, map[string]bool) {
	// Most jobs read a.log or b.log, so that many form groups.
	files := []string{"a.log", "a.log", "a.log", "b.log", "b.log", "./a.log", "a.log,b.log",
		"*.log", "x.csv", "*.csv", "x.csv.query", "sub/../y.csv", "link/a.log"}
	outfiles := []string{"x.csv", "./x.csv", "y.csv", "sub/../y.csv", "x.csv.query", "link/x.csv",
		"a.log", "o1.csv", "o2.csv", "o3.csv", "o4.csv"}
	servers := [][]string{nil, {"remote:2222"}}
	timeRanges := [][2]int{{0, 24}, {0, 24}, {0, 12}, {12, 13}, {13, 24}}

	jobs := make([]config.Scheduled, 2+rng.IntN(6))
	fails := map[string]bool{}
	for i := range jobs {
		job := &jobs[i]
		job.Name = fmt.Sprintf("j%d", i)
		job.Query = job.Name
		job.Enable = rng.IntN(10) != 0
		job.TimeRange = timeRanges[rng.IntN(len(timeRanges))]
		job.Files = files[rng.IntN(len(files))]
		job.Outfile = outfiles[rng.IntN(len(outfiles))]
		job.Servers = servers[rng.IntN(len(servers))]
		fails[job.Name] = rng.IntN(8) == 0
	}
	return jobs, fails
}

// setUpModelDir creates the log files, some outfiles of an earlier run and a
// link to the working directory in a new working directory.
func setUpModelDir(t *testing.T, dir string, rng *rand.Rand) {
	t.Helper()
	for _, sub := range []string{dir, filepath.Join(dir, "sub")} {
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(dir, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.log", "b.log"} {
		if err := os.WriteFile(name, []byte(name+" content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"x.csv", "y.csv"} {
		if rng.IntN(4) == 0 {
			if err := os.WriteFile(name, []byte("earlier run"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// The scheduler runs jobs on the same files together, yet the same jobs run
// and leave the same files, with the same contents, as when every job ran on
// its own in the configured order, for random job lists: jobs reading and
// writing the same files, outfiles named in different ways, outfiles that
// exist, jobs out of their time range, disabled, failing or on other servers.
func TestSchedulerGroupsGiveTheResultsOfJobsRunOneByOne(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	now := time.Date(2026, 9, 21, 12, 30, 0, 0, time.Local)
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, 0))

	grouped := 0
	for iteration := range 2000 {
		jobs, fails := randomJobs(rng)
		dirSeed := rng.Uint64()

		setUpModelDir(t, filepath.Join(root, fmt.Sprintf("%d-one-by-one", iteration)), rand.New(rand.NewPCG(dirSeed, 1)))
		want := &modelJobs{fails: fails}
		runSequentially(jobs, now, want)
		wantFiles := dirContents(t)

		setUpModelDir(t, filepath.Join(root, fmt.Sprintf("%d-grouped", iteration)), rand.New(rand.NewPCG(dirSeed, 1)))
		got := &modelJobs{fails: fails}
		s := newScheduler(config.RuntimeConfig{Server: &config.ServerConfig{
			SSHBindAddress: "127.0.0.1", Schedule: jobs,
		}}, jobTestLoggers)
		s.now = func() time.Time { return now }
		s.newMaprClient = got.newClient
		s.runJobs(context.Background())
		gotFiles := dirContents(t)

		wantRan, gotRan := slices.Sorted(slices.Values(want.ran)), slices.Sorted(slices.Values(got.ran))
		if !reflect.DeepEqual(gotRan, wantRan) || !reflect.DeepEqual(gotFiles, wantFiles) {
			var config strings.Builder
			for _, job := range jobs {
				fmt.Fprintf(&config, "\n  %s enable=%v range=%v files=%q outfile=%q servers=%v fails=%v",
					job.Name, job.Enable, job.TimeRange, job.Files, job.Outfile, job.Servers, fails[job.Name])
			}
			t.Fatalf("iteration %d, jobs:%s\nran %v, one by one %v\nfiles %v\none by one %v",
				iteration, config.String(), got.ran, want.ran, gotFiles, wantFiles)
		}
		if !slices.Equal(got.ran, want.ran) {
			grouped++
		}
	}
	// The random job lists must exercise groups that change the order.
	if grouped == 0 {
		t.Error("no job list ran in another order than one by one")
	}
	t.Logf("%d job lists ran in another order than one by one", grouped)
}
