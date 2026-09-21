package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

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
// A job that writes the same outfile as a job of the group stays pending, so
// that it runs after the group and, as when every job ran on its own, only if
// the group did not write the outfile. dserver bounds how many members share
// a read by its own cat slots; the other members read on their own.
func (s *scheduler) nextGroup(pending []*config.Scheduled) ([]dueJob, []*config.Scheduled) {
	now := s.now()
	first, reason := s.evaluate(pending[0], now)
	if reason != "" {
		s.log().Debug(pending[0].Name, reason)
		return nil, pending[1:]
	}
	group := []dueJob{first}
	key := first.groupKey()
	outfiles := map[string]bool{outfileKey(first.outfile): true}
	var rest []*config.Scheduled
	for _, job := range pending[1:] {
		due, reason := s.evaluate(job, now)
		if reason != "" || due.groupKey() != key || outfiles[outfileKey(due.outfile)] {
			rest = append(rest, job)
			continue
		}
		outfiles[outfileKey(due.outfile)] = true
		group = append(group, due)
	}
	return group, rest
}

// outfileKey returns the file an outfile path names: the absolute, cleaned
// path, with the symbolic links of the path, or of its directory while the
// outfile does not exist, resolved. Two jobs writing ./x.csv and x.csv write
// the same file.
func outfileKey(outfile string) string {
	path, err := filepath.Abs(outfile)
	if err != nil {
		return filepath.Clean(outfile)
	}
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
