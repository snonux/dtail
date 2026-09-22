package jobs

import (
	"fmt"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

const (
	// failedJobInitialBackoff is how long the scheduler waits after a job's
	// first failed run before it runs the job again. Every further failure in
	// a row doubles the wait, up to failedJobMaxBackoff.
	failedJobInitialBackoff = time.Minute
	failedJobMaxBackoff     = time.Hour
	// failedJobBackoffSlack lets a job run on a scheduler run that comes a
	// little before its backoff ends. The scheduler runs every minute, but
	// its runs, and the start of a job within them, drift by a little: the
	// slack keeps a job from missing the scheduler run one backoff after
	// its failed run by that little and waiting a whole minute longer.
	failedJobBackoffSlack = 5 * time.Second
)

// jobBackoff holds, in memory, the scheduled jobs whose last run failed and
// when the scheduler may run each again. A failed job wrote no outfile, so
// the scheduler would otherwise run it again every minute, and a job that
// fails for good (e.g. one of its servers is gone) would read the files of
// all its other servers every minute. The backoff of a job ends when it
// succeeds, and when its outfile changes (the dates filled into it moved on
// to a new period), as that is another result.
type jobBackoff struct {
	mu     sync.Mutex
	failed map[*config.Scheduled]failedJob
}

// failedJob is a job whose runs failed failures times in a row for outfile.
type failedJob struct {
	outfile  string
	failures int
	// retryAt is when the job may run again: the backoff after the start of
	// its last failed run, but at least half the backoff after its end, so
	// that a job running longer than its backoff does not run back to back.
	retryAt time.Time
}

// backoffDelay returns how long the scheduler waits before it runs a job
// again that failed failures times in a row: 1, 2, 4, ... minutes, at most an
// hour.
func backoffDelay(failures int) time.Duration {
	delay := failedJobInitialBackoff
	for i := 1; i < failures && delay < failedJobMaxBackoff; i++ {
		delay *= 2
	}
	return min(delay, failedJobMaxBackoff)
}

// wait returns why job, due at now with outfile, must not run yet, or "" if
// it may run. It forgets the job's failures once its outfile changed.
func (b *jobBackoff) wait(job *config.Scheduled, outfile string, now time.Time) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.failed[job]
	if !ok {
		return ""
	}
	if state.outfile != outfile {
		delete(b.failed, job)
		return ""
	}
	if now.Add(failedJobBackoffSlack).Before(state.retryAt) {
		return fmt.Sprintf("Not running job after failure %d in a row before about %s",
			state.failures, state.retryAt.Format(time.DateTime))
	}
	return ""
}

// fail records that the run of job with outfile, which started at started,
// failed and ended at ended. It returns how often the job failed in a row now
// and when it may run again.
func (b *jobBackoff) fail(job *config.Scheduled, outfile string, started, ended time.Time) failedJob {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed == nil {
		b.failed = make(map[*config.Scheduled]failedJob)
	}
	state := b.failed[job]
	if state.outfile != outfile {
		state = failedJob{outfile: outfile}
	}
	state.failures++
	delay := backoffDelay(state.failures)
	state.retryAt = started.Add(delay)
	if afterEnd := ended.Add(delay / 2); afterEnd.After(state.retryAt) {
		state.retryAt = afterEnd
	}
	b.failed[job] = state
	return state
}

// succeed forgets the failures of job.
func (b *jobBackoff) succeed(job *config.Scheduled) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.failed, job)
}
