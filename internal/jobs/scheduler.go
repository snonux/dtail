package jobs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/omode"

	gossh "golang.org/x/crypto/ssh"
)

type scheduler struct {
	cfg           config.RuntimeConfig
	logger        logging.Logger
	newMaprClient func(config.Args, clients.MaprClientMode) (backgroundClient, error)
	// now is the clock the time ranges and the dates in file names and
	// outfiles are evaluated with.
	now func() time.Time
}

func newScheduler(cfg config.RuntimeConfig, loggers clients.LoggerDependencies, colorizers ...*brush.Brush) *scheduler {
	colorizer := firstColorizer(colorizers)
	return &scheduler{
		cfg:    cfg,
		logger: logging.OrNop(loggers.Server),
		now:    time.Now,
		newMaprClient: func(args config.Args, mode clients.MaprClientMode) (backgroundClient, error) {
			return clients.NewMaprClient(args, cfg, mode, loggers, colorizer)
		},
	}
}

func (s *scheduler) log() logging.Logger {
	if s.logger == nil {
		return logging.NopLogger{}
	}
	return s.logger
}

func (s *scheduler) start(ctx context.Context) {
	s.log().Info("Starting scheduled job runner after 2s")
	if !wait(ctx, 2*time.Second) {
		return
	}
	s.runJobs(ctx)
	runTicker := time.NewTicker(time.Minute)
	defer runTicker.Stop()
	for {
		select {
		case <-runTicker.C:
			s.runJobs(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// runJobs runs the enabled jobs that are due, one group after another (see
// nextGroup). Like the scheduler that ran every job on its own, it checks a
// job's time range, fills the dates into its files and outfile and checks
// that the outfile does not exist yet right before the job's group starts.
func (s *scheduler) runJobs(ctx context.Context) {
	var pending []*config.Scheduled
	for i := range s.cfg.Server.Schedule {
		job := &s.cfg.Server.Schedule[i]
		if !job.Enable {
			s.log().Debug(job.Name, "Not running job as not enabled")
			continue
		}
		pending = append(pending, job)
	}
	for len(pending) > 0 {
		if ctx.Err() != nil {
			return
		}
		var group []dueJob
		group, pending = s.nextGroup(pending)
		if len(group) > 0 {
			s.runGroup(ctx, group)
		}
	}
}

// runJob runs job now unless its outfile exists; it does not check the job's
// time range.
func (s *scheduler) runJob(ctx context.Context, job *config.Scheduled) {
	if due, reason := s.prepare(job, s.now()); reason == "" {
		s.runDueJob(ctx, due)
	} else {
		s.log().Debug(job.Name, reason)
	}
}

// evaluate returns the client arguments of job at now, or why job is not due:
// now is outside its time range, or its outfile already exists.
func (s *scheduler) evaluate(job *config.Scheduled, now time.Time) (dueJob, string) {
	if hour := now.Hour(); hour < job.TimeRange[0] || hour >= job.TimeRange[1] {
		return dueJob{}, "Not running job out of time range"
	}
	return s.prepare(job, now)
}

// prepare returns the client arguments of job at now, or why job is not due:
// its outfile already exists.
func (s *scheduler) prepare(job *config.Scheduled, now time.Time) (dueJob, string) {
	files := fillDatesAt(job.Files, now)
	outfile := fillDatesAt(job.Outfile, now)

	_, err := os.Stat(outfile)
	if !os.IsNotExist(err) {
		return dueJob{}, "Not running job as outfile already exists: " + outfile
	}

	servers := strings.Join(job.Servers, ",")
	if servers == "" {
		servers = s.cfg.Server.SSHBindAddress
	}
	args := config.Args{
		ConnectionsPerCPU: config.DefaultConnectionsPerCPU,
		Discovery:         job.Discovery,
		ServersStr:        servers,
		What:              files,
		Mode:              omode.MapClient,
		SSHArgs: config.SSHArgs{
			NoAuthKey: true,
			UserName:  config.ScheduleUser,
		},
	}

	args.SSHAuthMethods = append(args.SSHAuthMethods, gossh.Password(job.Name))
	args.QueryStr = fmt.Sprintf("%s outfile %s", job.Query, outfile)
	return dueJob{job: job, args: args, outfile: outfile}, ""
}

func (s *scheduler) runDueJob(ctx context.Context, due dueJob) {
	job := due.job
	client, err := s.newMaprClient(due.args, clients.CumulativeMode)
	if err != nil {
		s.log().Error(fmt.Sprintf("Unable to create job %s", job.Name), err)
		return
	}

	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.log().Info(fmt.Sprintf("Starting job %s", job.Name))
	status := client.Start(jobCtx, make(chan string))
	logMessage := fmt.Sprintf("Job %s exited with status %d", job.Name, status)

	if status != 0 {
		s.log().Warn(logMessage)
		return
	}

	s.log().Info(logMessage)
}
