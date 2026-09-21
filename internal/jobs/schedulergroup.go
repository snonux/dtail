package jobs

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mimecast/dtail/internal/config"
)

// dueJob is a scheduled job that runs in the current scheduler run.
type dueJob struct {
	job     *config.Scheduled
	args    config.Args
	outfile string
	// groupLimit is the largest group the job may run in.
	groupLimit int
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

// groupLimit returns how many jobs reading the files of due may run as one
// group: as many as the server's cat slots let read all their files at the
// same time. Every member takes a slot per file before it joins the group
// read, and a member that gets its slots only after the read started reads
// on its own, after the group waited for it.
func (s *scheduler) groupLimit(due dueJob) int {
	files := len(strings.Split(due.args.What, ","))
	return max(1, s.cfg.Server.MaxConcurrentCats/files)
}

// groupDueJobs groups due jobs by the files, servers and discovery they read
// with, in the order of each group's first job; a group has at most its
// jobs' groupLimit members, further jobs form further groups. A job whose
// outfile an earlier job of the run writes too runs on its own and after that
// job, as it did when every job ran one after another: it runs only if the
// earlier job did not write the outfile.
func groupDueJobs(due []dueJob) [][]dueJob {
	var groups [][]dueJob
	openGroup := make(map[jobGroupKey]int)
	outfiles := make(map[string]bool)
	for _, job := range due {
		if outfiles[job.outfile] {
			groups = append(groups, []dueJob{job})
			continue
		}
		outfiles[job.outfile] = true
		key := job.groupKey()
		if index, ok := openGroup[key]; ok && len(groups[index]) < job.groupLimit {
			groups[index] = append(groups[index], job)
			continue
		}
		openGroup[key] = len(groups)
		groups = append(groups, []dueJob{job})
	}
	return groups
}

// runGroup runs the jobs of one group. A single job runs as before, checking
// its outfile again. The jobs of a larger group run concurrently and ask
// dserver, with a read share that is random for this run, to read their files
// once for the whole group; a dserver without shared reads ignores the share
// and every job reads on its own.
func (s *scheduler) runGroup(ctx context.Context, group []dueJob) {
	if len(group) == 1 {
		s.runJob(ctx, group[0].job)
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
