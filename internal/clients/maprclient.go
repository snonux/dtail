package clients

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr"
	maprclient "github.com/mimecast/dtail/internal/mapr/client"
	"github.com/mimecast/dtail/internal/omode"
)

// MaprClientMode determines whether to use cumulative mode or not.
type MaprClientMode int

const (
	// DefaultMode behaviour
	DefaultMode MaprClientMode = iota
	// CumulativeMode means results are added to prev interval
	CumulativeMode MaprClientMode = iota
	// NonCumulativeMode means results are from 0 for each interval
	NonCumulativeMode MaprClientMode = iota
)

// MaprClient is used for running mapreduce aggregations on remote files.
type MaprClient struct {
	baseClient
	// Shared mapreduce state for all handlers and reporting paths.
	session *maprclient.SessionState
	// Selected cumulative reporting mode.
	mode MaprClientMode
}

// NewMaprClient returns a new mapreduce client.
func NewMaprClient(args config.Args, maprClientMode MaprClientMode) (*MaprClient, error) {
	if args.QueryStr == "" {
		return nil, errors.New("No mapreduce query specified, use '-query' flag")
	}

	query, err := mapr.NewQuery(args.QueryStr)
	if err != nil {
		dlog.Client.FatalPanic(args.QueryStr, "Can't parse mapr query", err)
	}

	// Don't retry connection if in tail mode and no outfile specified.
	retry := args.Mode == omode.TailClient && !query.HasOutfile()

	c := MaprClient{
		baseClient: baseClient{
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      retry,
			runtime:    newClientRuntimeBoundary(config.CurrentRuntime()),
		},
		session: maprclient.NewSessionState(query),
		mode:    maprClientMode,
	}
	dlog.Client.Debug("Cumulative mapreduce mode?", c.isCumulative(query))

	c.setRegexForQuery(query)
	c.baseClient.init()
	c.baseClient.makeConnections(&c)

	return &c, nil
}

// Start starts the mapreduce client.
func (c *MaprClient) Start(ctx context.Context, statsCh <-chan string) (status int) {
	go c.periodicReportResults(ctx)

	status = c.baseClient.Start(ctx, statsCh)

	// Always write final result for cumulative mode (includes outfile case)
	if snapshot := c.session.Snapshot(); c.isCumulative(snapshot.Query) {
		dlog.Client.Debug("Writing final mapreduce result")
		if err := c.reportResults(true); err != nil {
			dlog.Client.Error("Unable to write final mapreduce result", err)
		}
		dlog.Client.Debug("Final result written")
	}

	return
}

// NEXT: Make this a callback function rather trying to use polymorphism to call
// this. This applies to all clients. It will make the code easier to read.
func (c *MaprClient) makeHandler(server string) handlers.Handler {
	return handlers.NewMaprHandler(server, c.session)
}

func (c *MaprClient) makeSessionSpec() (SessionSpec, error) {
	sessionSpec := NewSessionSpec(c.Args)
	if snapshot := c.session.Snapshot(); snapshot.Query != nil {
		sessionSpec.Query = snapshot.Query.RawQuery
	}
	return sessionSpec, nil
}

func (c *MaprClient) makeCommands() (commands []string) {
	sessionSpec, err := c.makeSessionSpec()
	if err != nil {
		dlog.Client.FatalPanic("unable to build map session spec", err)
	}
	commands, err = sessionSpec.Commands()
	if err != nil {
		dlog.Client.FatalPanic("unable to build map commands from session spec", err)
	}
	return commands
}

func (c *MaprClient) periodicReportResults(ctx context.Context) {
	var (
		lastGeneration uint64
		seenGeneration bool
	)

	for {
		snapshot := c.session.Snapshot()
		rampUp := !seenGeneration || snapshot.Generation != lastGeneration
		lastGeneration = snapshot.Generation
		seenGeneration = true

		delay := c.reportDelay(snapshot.Query, rampUp)
		dlog.Client.Debug("Sleeping before processing mapreduce results", "generation", snapshot.Generation, "delay", delay)

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
			dlog.Client.Debug("Gathering interim mapreduce result")
			if err := c.reportResults(false); err != nil {
				dlog.Client.Error("Unable to gather mapreduce result", err)
			}
		case <-c.session.Changes():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			dlog.Client.Debug("Mapreduce query generation changed, recalculating report interval")
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func (c *MaprClient) reportResults(finalResult bool) error {
	snapshot := c.session.Snapshot()
	if snapshot.Query == nil || snapshot.GlobalGroup == nil {
		return nil
	}

	if snapshot.Query.HasOutfile() {
		return c.writeResultsToOutfile(snapshot, finalResult)
	}
	return c.printResults(snapshot)
}

func (c *MaprClient) printResults(snapshot maprclient.SessionSnapshot) error {
	var result string
	var err error
	var numRows int
	rowsLimit := -1

	if snapshot.Query.Limit == -1 {
		// Limit output to 10 rows when the result is printed to stdout.
		// This can be overriden with the limit clause though.
		rowsLimit = 10
	}

	if c.isCumulative(snapshot.Query) {
		result, numRows, err = snapshot.GlobalGroup.Result(snapshot.Query, rowsLimit, c.runtime.output.MaprResultRenderer())
	} else {
		result, numRows, err = snapshot.GlobalGroup.SwapOut().Result(snapshot.Query, rowsLimit, c.runtime.output.MaprResultRenderer())
	}
	if err != nil {
		return fmt.Errorf("unable to render mapreduce result: %w", err)
	}

	changed, ok := c.session.CommitRenderedResult(snapshot.Generation, result)
	if !ok {
		dlog.Client.Debug("Discarding stale mapreduce result", "generation", snapshot.Generation)
		return nil
	}
	if !changed {
		dlog.Client.Debug("Result hasn't changed compared to last time...")
		return nil
	}

	if numRows == 0 {
		dlog.Client.Debug("Empty result set this time...")
		return nil
	}

	rawQuery := c.runtime.output.PaintMaprRawQuery(snapshot.Query.RawQuery)
	dlog.Client.Raw(fmt.Sprintf("%s\n", rawQuery))

	if rowsLimit > 0 && numRows > rowsLimit {
		dlog.Client.Warn(fmt.Sprintf("Got %d results but limited terminal output "+
			"to %d rows! Use 'limit' clause to override!", numRows, rowsLimit))
	}
	dlog.Client.Raw(fmt.Sprintf("%s\n", result))
	return nil
}

func (c *MaprClient) writeResultsToOutfile(snapshot maprclient.SessionSnapshot, finalResult bool) error {
	cumulative := c.isCumulative(snapshot.Query)
	dlog.Client.Debug("writeResultsToOutfile called", "finalResult", finalResult, "cumulative", cumulative, "generation", snapshot.Generation)
	if cumulative {
		if err := snapshot.GlobalGroup.WriteResult(snapshot.Query, finalResult); err != nil {
			return fmt.Errorf("unable to write cumulative mapreduce result: %w", err)
		}
		dlog.Client.Debug("WriteResult completed for cumulative mode")
		return nil
	}
	if err := snapshot.GlobalGroup.SwapOut().WriteResult(snapshot.Query, true); err != nil {
		return fmt.Errorf("unable to write non-cumulative mapreduce result: %w", err)
	}
	dlog.Client.Debug("WriteResult completed for non-cumulative mode")
	return nil
}

func (c *MaprClient) commitSessionSpec(spec SessionSpec, generation uint64) error {
	if spec.Query == "" {
		return errors.New("missing mapreduce query")
	}

	query, err := c.session.CommitQuery(spec.Query, generation)
	if err != nil {
		return err
	}

	c.Args.QueryStr = spec.Query
	c.setRegexForQuery(query)
	return nil
}

func (c *MaprClient) isCumulative(query *mapr.Query) bool {
	switch c.mode {
	case CumulativeMode:
		return true
	case NonCumulativeMode:
		return false
	default:
		return c.Args.Mode == omode.MapClient || (query != nil && query.HasOutfile())
	}
}

func (c *MaprClient) setRegexForQuery(query *mapr.Query) {
	if query == nil {
		c.RegexStr = "."
		return
	}

	switch query.Table {
	case "", ".":
		c.RegexStr = "."
	case "*":
		c.RegexStr = "\\|MAPREDUCE:\\|"
	default:
		c.RegexStr = fmt.Sprintf("\\|MAPREDUCE:%s\\|", query.Table)
	}
}

func (c *MaprClient) reportDelay(query *mapr.Query, rampUp bool) time.Duration {
	interval := time.Second
	if query != nil && query.Interval > 0 {
		interval = query.Interval
	}
	if !rampUp {
		return interval
	}

	delay := interval / 2
	if delay <= 0 {
		return interval
	}
	return delay
}
