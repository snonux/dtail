package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/omode"
	gossh "golang.org/x/crypto/ssh"
)

type continuousClient interface {
	Start(context.Context, <-chan string) int
}

type continuous struct {
	cfg              config.RuntimeConfig
	newMaprClient    func(config.Args, clients.MaprClientMode) (continuousClient, error)
	dayChangeWatcher func(context.Context) bool
	retryInterval    time.Duration
	now              func() time.Time
	newTicker        func(time.Duration) (<-chan time.Time, func())
}

func newContinuous(cfg config.RuntimeConfig) *continuous {
	c := &continuous{cfg: cfg}
	c.retryInterval = time.Minute
	c.now = time.Now
	c.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		ticker := time.NewTicker(d)
		return ticker.C, ticker.Stop
	}
	c.newMaprClient = func(args config.Args, mode clients.MaprClientMode) (continuousClient, error) {
		return clients.NewMaprClient(args, mode)
	}
	c.dayChangeWatcher = c.waitForDayChange
	return c
}

func (c *continuous) start(ctx context.Context) {
	dlog.Server.Info("Starting continuous job runner after 2s")
	time.Sleep(time.Second * 2)
	c.runJobs(ctx)
}

func (c *continuous) runJobs(ctx context.Context) {
	for i := range c.cfg.Server.Continuous {
		job := &c.cfg.Server.Continuous[i]
		if !job.Enable {
			dlog.Server.Debug(job.Name, "Not running job as not enabled")
			continue
		}
		go func(job *config.Continuous) {
			c.runJob(ctx, job)
			retryTicker := time.NewTicker(c.retryInterval)
			defer retryTicker.Stop()
			for {
				select {
				// Retry after the configured interval.
				case <-retryTicker.C:
					c.runJob(ctx, job)
				case <-ctx.Done():
					return
				}
			}
		}(job)
	}
}

func (c *continuous) runJob(ctx context.Context, job *config.Continuous) {
	dlog.Server.Debug(job.Name, "Processing job")

	files := fillDates(job.Files)
	outfile := fillDates(job.Outfile)
	servers := strings.Join(job.Servers, ",")
	if servers == "" {
		servers = c.cfg.Server.SSHBindAddress
	}

	args := config.Args{
		ConnectionsPerCPU: config.DefaultConnectionsPerCPU,
		Discovery:         job.Discovery,
		ServersStr:        servers,
		What:              files,
		Mode:              omode.TailClient,
		UserName:          config.ContinuousUser,
	}

	args.SSHAuthMethods = append(args.SSHAuthMethods, gossh.Password(job.Name))
	args.QueryStr = fmt.Sprintf("%s outfile %s", job.Query, outfile)
	client, err := c.newMaprClient(args, clients.NonCumulativeMode)
	if err != nil {
		dlog.Server.Error(fmt.Sprintf("Unable to create job %s", job.Name), err)
		return
	}

	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if job.RestartOnDayChange {
		go func() {
			if c.dayChangeWatcher(jobCtx) {
				dlog.Server.Info(fmt.Sprintf("Canceling job %s due to day change", job.Name))
				cancel()
			}
		}()
	}

	dlog.Server.Info(fmt.Sprintf("Starting job %s", job.Name))
	status := client.Start(jobCtx, make(chan string))
	logMessage := fmt.Sprintf("Job exited with status %d", status)
	if status != 0 {
		dlog.Server.Warn(logMessage)
		return
	}
	dlog.Server.Info(logMessage)
}

func (c *continuous) waitForDayChange(ctx context.Context) bool {
	startTime := c.now()
	tickCh, stop := c.newTicker(time.Second)
	defer stop()
	for {
		select {
		case <-tickCh:
			if !sameCalendarDay(c.now(), startTime) {
				return true
			}
		case <-ctx.Done():
			return false
		}
	}
}

func sameCalendarDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
