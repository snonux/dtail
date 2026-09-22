package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/discovery"
)

// dueJob is a scheduled job that runs in the current scheduler run.
type dueJob struct {
	job     *config.Scheduled
	args    config.Args
	outfile string
	// rangeEnd is when the job's TimeRange that the run is in ends.
	rangeEnd time.Time
	// final is set for a run after the TimeRange of a failed run ended
	// (see jobBackoff): it writes what it could read.
	final bool
}

// jobGroupKey identifies the jobs that read the same files from the same
// servers: they run together and let dserver read each file once for all
// of them.
type jobGroupKey struct {
	files     string
	servers   string
	discovery string
}

func (d dueJob) groupKey() jobGroupKey {
	return jobGroupKey{files: d.args.What, servers: d.args.ServersStr, discovery: d.args.Discovery}
}

// pendingRun is a run the scheduler may start in its current run: a job run
// within the job's TimeRange (scheduledRun) or the final run of a failed job
// (finalRun).
type pendingRun interface {
	// name is the name of the run's job.
	name() string
	// evaluate returns the client arguments of the run at now, or why it is
	// not due.
	evaluate(s *scheduler, now time.Time) (dueJob, string)
	// footprint returns what the run reads and writes at now.
	footprint(now time.Time) jobFootprint
}

// scheduledRun is a run of job within its TimeRange.
type scheduledRun struct {
	job *config.Scheduled
}

func (r scheduledRun) name() string { return r.job.Name }

func (r scheduledRun) evaluate(s *scheduler, now time.Time) (dueJob, string) {
	return s.evaluate(r.job, now)
}

func (r scheduledRun) footprint(now time.Time) jobFootprint {
	return footprintAt(r.job, now)
}

// finalRun is the final run of a job whose runs failed (see jobBackoff),
// with the files and the outfile of its last failed run within its TimeRange.
type finalRun struct {
	state failedJob
}

func (r finalRun) name() string { return r.state.due.job.Name }

// evaluate returns the final run, or why it is not due: its outfile exists by
// now, and then the job's failures for it are forgotten.
func (r finalRun) evaluate(s *scheduler, _ time.Time) (dueJob, string) {
	if _, err := os.Stat(r.state.due.outfile); !os.IsNotExist(err) {
		s.backoff.forget(r.state)
		return dueJob{}, "Not running final run as outfile already exists: " + r.state.due.outfile
	}
	due := r.state.due
	due.final = true
	due.rangeEnd = r.state.rangeEnd
	return due, ""
}

func (r finalRun) footprint(time.Time) jobFootprint {
	return footprintOf(r.state.due.args.What, r.state.due.outfile)
}

// nextGroup evaluates the first pending run at the current time and, if it
// is due, groups it with the pending runs that are due at the same time and
// read the same files from the same servers with the same discovery. It
// returns the group, empty when the first run is not due, and the runs still
// pending, in their order; those are evaluated again when their group starts.
//
// The runs of a group run together, before the runs that stay pending. So
// that the same jobs run and write the same outfiles as when every run ran on
// its own in order, a run joins the group only if it does not conflict (see
// jobFootprint.conflicts) with a run of the group or with an earlier run that
// stays pending. A run that writes the outfile of such a run thus stays
// pending, runs after it and, as before, only if that run did not write the
// outfile. dserver bounds how many members share a read by its own cat slots;
// the other members read on their own.
//
// A group has at most groupLimit runs; the other runs that could join it stay
// pending and form the next groups, each a wave of at most groupLimit runs
// sharing reads among themselves.
func (s *scheduler) nextGroup(ctx context.Context, pending []pendingRun) ([]dueJob, []pendingRun) {
	now := s.now()
	first, reason := pending[0].evaluate(s, now)
	if reason != "" {
		s.log().Debug(pending[0].name(), reason)
		return nil, pending[1:]
	}
	group := []dueJob{first}
	key := first.groupKey()
	limit := s.groupLimit(ctx, first.args)
	// before holds the footprints of the group's runs and of the earlier
	// runs that stay pending: the runs a later run must not conflict with to
	// join the group.
	before := []jobFootprint{pending[0].footprint(now)}
	var rest []pendingRun
	for _, run := range pending[1:] {
		footprint := run.footprint(now)
		due, reason := run.evaluate(s, now)
		if len(group) >= limit || reason != "" || due.groupKey() != key || footprint.conflictsWithAny(before) {
			rest = append(rest, run)
		} else {
			group = append(group, due)
		}
		before = append(before, footprint)
	}
	return group, rest
}

// runPending runs pending, one group after another (see nextGroup).
func (s *scheduler) runPending(ctx context.Context, pending []pendingRun) {
	for len(pending) > 0 {
		if ctx.Err() != nil {
			return
		}
		var group []dueJob
		group, pending = s.nextGroup(ctx, pending)
		if len(group) > 0 {
			s.runGroup(ctx, group)
		}
	}
}

// groupLimit returns how many jobs connecting to the servers of args may run
// together.
//
// Jobs run together only when every server of args reaches the dserver
// running the scheduler (see thisDServer.reaches), whose MaxConnections the
// scheduler knows, and when that dserver shares reads (Server.SharedReadsDisable
// is not set): without a shared read, running jobs together only adds load. At
// most a quarter of MaxConnections, divided by the number of servers each job
// connects to, and at least one, run together then.
//
// Every job of a group opens an SSH connection of its own to each of its
// servers, and dserver counts connections still in their handshake against
// MaxConnections too, and refuses the others; a refused job fails and runs
// again only after its backoff (see jobBackoff). The quarter leaves the other
// connections to interactive users and continuous jobs. Dividing by the
// number of servers keeps the bound when several server names of a job reach
// this dserver. The limits of other dservers are unknown to the scheduler, and
// others may use up their connections: jobs on them, and jobs whose servers
// cannot be discovered, run one at a time, as the scheduler did before it
// grouped jobs.
func (s *scheduler) groupLimit(ctx context.Context, args config.Args) int {
	if s.cfg.Server.SharedReadsDisable {
		return 1
	}
	finder, err := discovery.New(args.Discovery, args.ServersStr, discovery.Shuffle, s.log())
	var servers []string
	if err == nil {
		servers, err = finder.ServerList()
	}
	if err != nil {
		s.log().Warn("Running jobs one at a time as their servers can not be discovered", err)
		return 1
	}
	if len(servers) == 0 {
		return 1
	}
	for _, server := range servers {
		if !s.thisDServer.reaches(ctx, server) {
			s.log().Debug("Running jobs one at a time as a server is not this dserver", server)
			return 1
		}
	}
	return max(1, s.cfg.Server.MaxConnections/4/len(servers))
}

// jobFootprint is what a scheduled job reads and writes at a time: the
// patterns of the files it reads and the files it writes, its outfile, the
// outfile's .query file and their temporary files, each as configured
// (absolute and cleaned) and with symbolic links resolved (see outfileKey).
type jobFootprint struct {
	reads  []string
	writes []string
}

func footprintAt(job *config.Scheduled, now time.Time) jobFootprint {
	return footprintOf(fillDatesAt(job.Files, now), fillDatesAt(job.Outfile, now))
}

// footprintOf returns the footprint of a job reading files, a comma separated
// list of patterns, and writing outfile, both with their dates filled in.
func footprintOf(files, outfile string) jobFootprint {
	var footprint jobFootprint
	for _, pattern := range strings.Split(files, ",") {
		if pattern = strings.TrimSpace(pattern); pattern == "" {
			continue
		}
		footprint.reads = append(footprint.reads, filePatterns(pattern)...)
	}
	for _, path := range []string{absPath(outfile), outfileKey(outfile)} {
		for _, suffix := range []string{"", ".tmp", ".query", ".query.tmp"} {
			footprint.writes = append(footprint.writes, path+suffix)
		}
	}
	return footprint
}

// filePatterns returns pattern absolute and cleaned, and with the symbolic
// links of its directory resolved.
func filePatterns(pattern string) []string {
	path := absPath(pattern)
	patterns := []string{path}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		patterns = append(patterns, filepath.Join(dir, filepath.Base(path)))
	}
	return patterns
}

// conflictsWithAny reports whether f conflicts with one of others.
func (f jobFootprint) conflictsWithAny(others []jobFootprint) bool {
	return slices.ContainsFunc(others, f.conflicts)
}

// conflicts reports whether the jobs of f and other may not run in another
// order than configured, nor together: they write a file in common, or one
// reads a file the other writes. Two jobs that do not conflict give the same
// results in any order.
func (f jobFootprint) conflicts(other jobFootprint) bool {
	for _, path := range f.writes {
		if slices.Contains(other.writes, path) {
			return true
		}
	}
	return readsAny(f.reads, other.writes) || readsAny(other.reads, f.writes)
}

// readsAny reports whether one of patterns matches one of paths. A malformed
// pattern matches every path.
func readsAny(patterns, paths []string) bool {
	for _, pattern := range patterns {
		for _, path := range paths {
			if matched, err := filepath.Match(pattern, path); matched || err != nil {
				return true
			}
		}
	}
	return false
}

func absPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

// outfileKey returns the file an outfile path names: the absolute, cleaned
// path, with the symbolic links of the path, or of its directory while the
// outfile does not exist, resolved. Two jobs writing ./x.csv and x.csv write
// the same file.
func outfileKey(outfile string) string {
	path := absPath(outfile)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(dir, filepath.Base(path))
	}
	return path
}

// runGroup runs the jobs of one group. A single job runs as before. The jobs
// of a larger group run concurrently and ask
// dserver, with a read share that is random for this run, to read their files
// once for the whole group; a dserver without shared reads ignores the share
// and every job reads on its own.
func (s *scheduler) runGroup(ctx context.Context, group []dueJob) {
	if len(group) == 1 {
		s.runDueJob(ctx, group[0])
		return
	}

	share, err := config.NewReadShare(len(group))
	if err != nil {
		s.log().Warn("Running job group without a read share", err)
	}
	names := make([]string, len(group))
	for i := range group {
		names[i] = group[i].job.Name
		group[i].args.ReadShare = share
	}
	kind := "job group"
	if group[0].final {
		kind = "final run group"
	}
	s.log().Info(fmt.Sprintf("Starting %s of %d jobs reading %s together", kind, len(group), group[0].args.What),
		"jobs="+strings.Join(names, ","))

	var wg sync.WaitGroup
	for _, due := range group {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runDueJob(ctx, due)
		}()
	}
	wg.Wait()
}
