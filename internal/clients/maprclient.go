package clients

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/constants"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/omode"
)

// MaprClientMode determines how MapReduce results are accumulated between
// periodic reporting intervals. This affects whether results build up over
// time or reset for each interval.
type MaprClientMode int

const (
	// DefaultMode uses the default behavior based on client mode and output settings.
	// Cumulative for MapClient or when outfile is specified, non-cumulative otherwise.
	DefaultMode MaprClientMode = iota
	
	// CumulativeMode accumulates results across intervals, adding new results
	// to previous totals. Useful for building aggregate statistics over time.
	CumulativeMode MaprClientMode = iota
	
	// NonCumulativeMode resets results for each interval, showing only the
	// data processed during that specific time period.
	NonCumulativeMode MaprClientMode = iota
)

// MaprClient provides distributed MapReduce functionality for log analysis
// and aggregation across multiple servers. It supports SQL-like queries with
// SELECT, FROM, WHERE, GROUP BY, and HAVING clauses for complex log analysis.
//
// Key features:
// - SQL-like query syntax for intuitive log analysis
// - Distributed aggregation with server-side local processing
// - Client-side final aggregation of results from all servers
// - Periodic result reporting with configurable intervals
// - Support for both cumulative and interval-based result modes
// - Output to files or terminal with configurable row limits
//
// MaprClient directly embeds baseClient for core functionality and implements
// specialized command generation and result processing for MapReduce operations.
type MaprClient struct {
	baseClient
	
	// globalGroup manages the merged aggregation results from all servers,
	// performing final client-side aggregation and result formatting
	globalGroup *mapr.GlobalGroupSet
	
	// query contains the parsed SQL-like query structure with all clauses
	// and configuration options extracted from the query string
	query *mapr.Query
	
	// cumulative determines whether results accumulate across intervals
	// (true) or reset for each reporting period (false)
	cumulative bool
	
	// lastResult caches the last formatted result string to avoid
	// duplicate output when results haven't changed
	lastResult string
}

// NewMaprClient creates a new MaprClient configured for distributed MapReduce operations.
// This constructor parses the SQL-like query, validates the configuration, and sets up
// the client for aggregation operations with the specified accumulation mode.
//
// Parameters:
//   args: Complete configuration arguments including servers, query string, and options
//   maprClientMode: How to handle result accumulation between intervals
//
// Returns:
//   *MaprClient: Configured client ready to start MapReduce operations
//   error: Query parsing or configuration error, if any
//
// Configuration process:
// - Validates and parses the SQL-like query string
// - Determines retry behavior based on mode and output settings
// - Sets cumulative mode based on maprClientMode parameter
// - Configures regex pattern based on query table specification
// - Initializes global aggregation state and server connections
//
// The returned client is fully initialized and ready to call Start().
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

	var cumulative bool
	switch maprClientMode {
	case CumulativeMode:
		cumulative = true
	case NonCumulativeMode:
		cumulative = false
	default:
		// Result is comulative if we are in MapClient mode or with outfile
		cumulative = args.Mode == omode.MapClient || query.HasOutfile()
	}

	dlog.Client.Debug("Cumulative mapreduce mode?", cumulative)

	c := MaprClient{
		baseClient: baseClient{
			Args:       args,
			throttleCh: make(chan struct{}, args.ConnectionsPerCPU*runtime.NumCPU()),
			retry:      retry,
		},
		query:      query,
		cumulative: cumulative,
	}

	switch c.query.Table {
	case "", ".":
		c.RegexStr = "."
	case "*":
		c.RegexStr = fmt.Sprintf("\\|MAPREDUCE:\\|")
	default:
		c.RegexStr = fmt.Sprintf("\\|MAPREDUCE:%s\\|", c.query.Table)
	}

	c.globalGroup = mapr.NewGlobalGroupSet()
	c.baseClient.init()
	c.baseClient.makeConnections(c)

	return &c, nil
}

// Start begins the MapReduce operation by launching periodic result reporting
// and initiating connections to all servers. This method coordinates the entire
// MapReduce lifecycle including query execution, result aggregation, and output.
//
// Parameters:
//   ctx: Context for cancellation and timeout control
//   statsCh: Channel for receiving statistics display requests
//
// Returns:
//   int: Exit status code (0 for success, non-zero for various error conditions)
//
// Operation flow:
// 1. Starts periodic result reporting in a separate goroutine
// 2. Launches base client connections to all servers
// 3. If in cumulative mode, reports final aggregated results
// 4. Returns the highest status code from any server connection
func (c *MaprClient) Start(ctx context.Context, statsCh <-chan string) (status int) {
	go c.periodicReportResults(ctx)

	status = c.baseClient.Start(ctx, statsCh)
	if c.cumulative {
		dlog.Client.Debug("Received final mapreduce result")
		c.reportResults()
	}

	return
}

// makeHandler creates a MapReduce-specific handler for processing aggregation
// operations on the specified server. This method implements the maker interface
// requirement and provides the handler used for MapReduce query execution.
//
// Parameters:
//   server: The server hostname/address for this handler
//
// Returns:
//   handlers.Handler: A MaprHandler configured for the specified server and query
//
// The returned handler manages MapReduce protocol communication, query execution,
// and local aggregation on the server side before sending results back to the client.
func (c MaprClient) makeHandler(server string) handlers.Handler {
	return handlers.NewMaprHandler(server, c.query, c.globalGroup)
}

// makeCommands generates the appropriate DTail server commands for MapReduce
// operations. This method implements the maker interface requirement and creates
// commands for distributed query execution across all specified files.
//
// Returns:
//   []string: List of commands to send to DTail servers
//
// Command generation process:
// 1. Creates a "map" command with the raw query string
// 2. Determines the appropriate mode (cat or tail) based on client configuration
// 3. Generates file-specific commands with regex patterns and timeouts
// 4. Includes all necessary options for proper server-side execution
//
// The generated commands follow the DTail protocol format and enable
// distributed MapReduce query execution across all target servers.
func (c MaprClient) makeCommands() (commands []string) {
	commands = append(commands, fmt.Sprintf("map %s", c.query.RawQuery))
	modeStr := "cat"
	if c.Mode == omode.TailClient {
		modeStr = "tail"
	}

	for _, file := range strings.Split(c.What, ",") {
		regex, err := c.Regex.Serialize()
		if err != nil {
			dlog.Client.FatalPanic(err)
		}
		if c.Timeout > 0 {
			commands = append(commands, fmt.Sprintf("timeout %d %s %s %s", c.Timeout,
				modeStr, file, regex))
			continue
		}
		commands = append(commands, fmt.Sprintf("%s:%s %s %s",
			modeStr, c.Args.SerializeOptions(), file, regex))
	}
	return
}

// periodicReportResults runs in a separate goroutine to provide regular
// result reporting at configured intervals. This method handles the timing
// and coordination of result aggregation and output during long-running
// MapReduce operations.
//
// Parameters:
//   ctx: Context for cancellation control
//
// Operation flow:
// 1. Waits for an initial ramp-up period (half the configured interval)
// 2. Reports results at regular intervals until context cancellation
// 3. Ensures results are available before the first reporting period
//
// This method is essential for providing real-time feedback during
// long-running aggregation operations.
func (c *MaprClient) periodicReportResults(ctx context.Context) {
	rampUpSleep := c.query.Interval / 2
	dlog.Client.Debug("Ramp up sleeping before processing mapreduce results", rampUpSleep)
	time.Sleep(rampUpSleep)

	for {
		select {
		case <-time.After(c.query.Interval):
			dlog.Client.Debug("Gathering interim mapreduce result")
			c.reportResults()
		case <-ctx.Done():
			return
		}
	}
}

// reportResults outputs the current aggregation results either to a file
// or to the terminal, depending on the query configuration. This method
// handles the final result formatting and output routing.
//
// Output routing:
// - If query specifies an output file, writes results to that file
// - Otherwise, formats and prints results to the terminal
//
// This method is called both periodically during operation and once
// at the end for final result output.
func (c *MaprClient) reportResults() {
	if c.query.HasOutfile() {
		c.writeResultsToOutfile()
		return
	}
	c.printResults()
}

// printResults formats and displays aggregation results to the terminal
// with appropriate formatting, coloring, and row limiting. This method
// handles all aspects of terminal output including duplicate detection
// and user-friendly result presentation.
//
// Terminal output features:
// - Colored query display when terminal colors are enabled
// - Automatic row limiting for terminal display (default 10 rows)
// - Duplicate result detection to avoid redundant output
// - Warning messages when results exceed display limits
// - Proper formatting of aggregated data tables
//
// This method is called when no output file is specified in the query.
func (c *MaprClient) printResults() {
	var result string
	var err error
	var numRows int
	rowsLimit := constants.MapReduceUnlimited

	if c.query.Limit == constants.MapReduceUnlimited {
		// Limit output to 10 rows when the result is printed to stdout.
		// This can be overriden with the limit clause though.
		rowsLimit = constants.DefaultMapReduceRowsLimit
	}

	if c.cumulative {
		result, numRows, err = c.globalGroup.Result(c.query, rowsLimit)
	} else {
		result, numRows, err = c.globalGroup.SwapOut().Result(c.query, rowsLimit)
	}
	if err != nil {
		dlog.Client.FatalPanic(err)
	}

	if result == c.lastResult {
		dlog.Client.Debug("Result hasn't changed compared to last time...")
		return
	}
	c.lastResult = result

	if numRows == 0 {
		dlog.Client.Debug("Empty result set this time...")
		return
	}

	rawQuery := c.query.RawQuery
	if config.Client.TermColorsEnable {
		rawQuery = color.PaintStrWithAttr(rawQuery,
			config.Client.TermColors.MaprTable.RawQueryFg,
			config.Client.TermColors.MaprTable.RawQueryBg,
			config.Client.TermColors.MaprTable.RawQueryAttr)
	}
	dlog.Client.Raw(fmt.Sprintf("%s\n", rawQuery))

	if rowsLimit > 0 && numRows > rowsLimit {
		dlog.Client.Warn(fmt.Sprintf("Got %d results but limited terminal output "+
			"to %d rows! Use 'limit' clause to override!", numRows, rowsLimit))
	}
	dlog.Client.Raw(fmt.Sprintf("%s\n", result))
}

// writeResultsToOutfile saves aggregation results to the file specified
// in the query configuration. This method handles file output with proper
// accumulation mode handling for persistent result storage.
//
// File output behavior:
// - Cumulative mode: Appends/updates results in the output file
// - Non-cumulative mode: Writes interval-specific results
// - Proper error handling for file operations
//
// This method is called when the query specifies an output file path,
// enabling long-term storage and analysis of aggregation results.
func (c *MaprClient) writeResultsToOutfile() {
	if c.cumulative {
		if err := c.globalGroup.WriteResult(c.query); err != nil {
			dlog.Client.FatalPanic(err)
		}
		return
	}
	if err := c.globalGroup.SwapOut().WriteResult(c.query); err != nil {
		dlog.Client.FatalPanic(err)
	}
}
