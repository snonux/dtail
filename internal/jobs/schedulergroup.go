package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

// dueJob is a scheduled job that runs in the current scheduler run.
type dueJob struct {
	job     *config.Scheduled
	args    config.Args
	outfile string
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

// nextGroup evaluates the first pending job at the current time and, if it
// is due, groups it with the pending jobs that are due at the same time and
// read the same files from the same servers with the same discovery. It
// returns the group, empty when the first job is not due, and the jobs still
// pending, in their order; those are evaluated again when their group starts.
//
// The jobs of a group run together, before the jobs that stay pending. So
// that the same jobs run and write the same outfiles as when every job ran on
// its own in the configured order, a job joins the group only if it does not
// conflict (see jobFootprint.conflicts) with a job of the group or with an
// earlier job that stays pending. A job that writes the outfile of such a job
// thus stays pending, runs after it and, as before, only if that job did not
// write the outfile. dserver bounds how many members share a read by its own
// cat slots; the other members read on their own.
func (s *scheduler) nextGroup(pending []*config.Scheduled) ([]dueJob, []*config.Scheduled) {
	now := s.now()
	first, reason := s.evaluate(pending[0], now)
	if reason != "" {
		s.log().Debug(pending[0].Name, reason)
		return nil, pending[1:]
	}
	group := []dueJob{first}
	key := first.groupKey()
	// before holds the footprints of the group's jobs and of the earlier
	// jobs that stay pending: the jobs a later job must not conflict with to
	// join the group.
	before := []jobFootprint{footprintAt(first.job, now)}
	var rest []*config.Scheduled
	for _, job := range pending[1:] {
		footprint := footprintAt(job, now)
		due, reason := s.evaluate(job, now)
		if reason != "" || due.groupKey() != key || footprint.conflictsWithAny(before) {
			rest = append(rest, job)
		} else {
			group = append(group, due)
		}
		before = append(before, footprint)
	}
	return group, rest
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
	var footprint jobFootprint
	for _, pattern := range strings.Split(fillDatesAt(job.Files, now), ",") {
		if pattern = strings.TrimSpace(pattern); pattern == "" {
			continue
		}
		footprint.reads = append(footprint.reads, filePatterns(pattern)...)
	}
	outfile := fillDatesAt(job.Outfile, now)
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
	s.log().Info(fmt.Sprintf("Starting job group of %d jobs reading %s together", len(group), group[0].args.What),
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
