package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mimecast/dtail/internal/mapr"
	maprclient "github.com/mimecast/dtail/internal/mapr/client"
)

// The results of MapReduce queries on the JSON control channel. A client
// with a result sink (MaprClient.resultSink) passes its results on as data
// instead of printing the table: as result events while a session runs a
// query (interactive dtail and dmap, emitControlResult), or as the result
// of the query request that ran it (QueryBuilderClient.runQuery).

// controlResult is one result of a query: the table as columns and rows of
// strings, formatted as the printed table's values.
type controlResult struct {
	Query   string     `json:"query"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	// RowsTotal is the number of rows of the result; more than len(Rows)
	// when the rows were cut to the query's limit clause or, without one,
	// to controlResultMaxRows (Truncated).
	RowsTotal int  `json:"rowsTotal"`
	Truncated bool `json:"truncated,omitempty"`
	// Final is set for the result after the query ended; an interim result
	// of a query still running is not final.
	Final bool `json:"final"`
	// Outfile is the file the result was written to instead, for a query
	// with an outfile clause; Columns and Rows are empty then.
	Outfile string `json:"outfile,omitempty"`
}

// controlResultEvent is the result event of a session that runs a query.
type controlResultEvent struct {
	Event string `json:"event"`
	controlResult
}

// controlQueryRun is the result of a query request that ran the query to
// its end (QueryBuilderClient.runQuery): the final result, the exit status
// of the run (as dmap --query has it) and what may have kept data out of
// the result.
type controlQueryRun struct {
	controlResult
	Status int `json:"status"`
	// Warnings are the reasons the result may be incomplete, per server: a
	// file that could not be read, a failed command, a session cut short.
	Warnings []string `json:"warnings,omitempty"`
	// Read is how much of the files the servers read for the result (the
	// read progress at the end of the run); nil when no server reported
	// any, e.g. servers of before the progress report.
	Read *controlRead `json:"read,omitempty"`
}

// controlRead is the read totals of a finished query run.
type controlRead struct {
	Bytes      int64 `json:"bytes"`
	Total      int64 `json:"total"`
	Files      int64 `json:"files"`
	FilesTotal int64 `json:"filesTotal"`
	Reporting  int   `json:"reporting"`
	Servers    int   `json:"servers"`
}

// newControlResult turns table into a result, its rows cut to the query's
// limit, or to controlResultMaxRows without one.
func newControlResult(query *mapr.Query, table mapr.ResultTable, final bool) controlResult {
	limit := controlResultMaxRows
	if query.Limit >= 0 {
		limit = query.Limit
	}
	result := controlResult{Query: query.RawQuery, Columns: table.Columns, Rows: table.Rows,
		RowsTotal: len(table.Rows), Final: final}
	if len(result.Rows) > limit {
		result.Rows = result.Rows[:limit]
		result.Truncated = true
	}
	if result.Rows == nil {
		result.Rows = [][]string{}
	}
	return result
}

// sinkResults passes the result of snapshot to the result sink instead of
// printing it (printResults): the swapped-out interval of a non-cumulative
// query, the whole result of a cumulative one. Like the printed table, an
// interim result is passed on only when it has rows and changed since the
// last one; the final result always is, so that its reader gets an answer.
func (c *MaprClient) sinkResults(snapshot maprclient.SessionSnapshot, final bool) error {
	var table mapr.ResultTable
	var err error
	if c.isCumulative(snapshot.Query) {
		table, err = snapshot.GlobalGroup.ResultTable(snapshot.Query)
	} else {
		table, err = snapshot.GlobalGroup.SwapOut().ResultTable(snapshot.Query)
	}
	if err != nil {
		return fmt.Errorf("unable to read mapreduce result: %w", err)
	}
	result := newControlResult(snapshot.Query, table, final)
	key, err := json.Marshal(result.Rows)
	if err != nil {
		return fmt.Errorf("unable to encode mapreduce result: %w", err)
	}
	changed, current := c.session.CommitRenderedResult(snapshot.Generation, string(key))
	if !current || (!final && (!changed || len(result.Rows) == 0)) {
		return nil
	}
	c.resultSink(result)
	return nil
}

// emitControlResult is the result sink of a session with a JSON control
// channel: every result is a result event.
func (c *MaprClient) emitControlResult(result controlResult) {
	c.control.write(controlResultEvent{Event: "result", controlResult: result})
}

// runQuery runs query over files as dmap --query does, on connections of
// its own next to the held sessions, and returns its final result, exit
// status and warnings (clientProfile.runQuery). It ends with ctx. Only
// such a run, a plain command stream the server ends with its close
// handshake, tells that a result is complete (see QueryBuilderClient).
func (c *QueryBuilderClient) runQuery(ctx context.Context, files, query string,
	progress func(progressReport)) (controlQueryRun, error) {

	runArgs := c.runArgs
	runArgs.What = files
	runArgs.QueryStr = query
	// A one-shot run without a control channel of its own, and without
	// periodic connection statistics in the session's output.
	runArgs.InteractiveQuery = false
	runArgs.Control, runArgs.ControlIn, runArgs.ControlOut = "", "", ""
	runArgs.Quiet = true
	runner, err := c.newQueryRunner(runArgs)
	if err != nil {
		return controlQueryRun{}, fmt.Errorf("unable to run the query: %w", err)
	}
	var mu sync.Mutex
	var run controlQueryRun
	runner.resultSink = func(result controlResult) {
		mu.Lock()
		defer mu.Unlock()
		run.controlResult = result
	}
	runner.stats.progressSink = progress
	status := runner.Start(ctx, nil)
	mu.Lock()
	defer mu.Unlock()
	run.Status = status
	run.Warnings = runner.resultWarnings()
	if totals := runner.progressTotals(); totals.Reporting > 0 {
		run.Read = &controlRead{Bytes: totals.Bytes, Total: totals.Total, Files: totals.Files,
			FilesTotal: totals.FilesTotal, Reporting: totals.Reporting, Servers: totals.Servers}
	}
	if run.Query == "" {
		// No final result was reported (the run was cancelled).
		run.controlResult = controlResult{Query: query, Columns: []string{}, Rows: [][]string{}}
	}
	return run, nil
}

// resultWarnings returns, each as "<server>: <reason>", why the result of a
// finished run may lack data: the reasons dmap warns about
// (incompleteResultReasons) and the files that could not be read.
func (c *MaprClient) resultWarnings() []string {
	warnings := c.incompleteResultReasons()
	for _, session := range c.snapshotSessions() {
		_, fileFailures := sessionOutcome(session.conn.Server(), session.conn.Handler())
		warnings = append(warnings, fileFailures...)
	}
	return warnings
}
