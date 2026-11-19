package clients

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// QueryBuilderClient is dmap started without --query on a terminal: it
// lets the user build the query first and then runs it. It works in two
// phases, neither of which needs anything of dserver that interactive dtail
// and dmap --query do not use already:
//
//  1. The builder: interactive sessions that hold no workload, on at most
//     probeMaxConnections servers (holdBuilderSessions; builderHoldSpec:
//     SESSION START in tail mode without files, as an interactive dtail
//     without --files sends it), with the control
//     loop, the line editor (interactiveEditor) and the background prober
//     (interactiveProber) of every interactive session. The prober probes
//     the files given with --files, which are not part of the held spec,
//     so nothing is read or printed. The line typed is the query
//     (handleBuilderLine). The held sessions are kept alive
//     (keepSessionsAlive), and one that ends all the same is replaced by
//     one to the next server, which only costs the probes meanwhile.
//  2. The run: once a query that parses was entered, the builder ends its
//     sessions and the client the query was built for takes over on new
//     connections: NewMaprClient with the built query and files, i.e. what
//     dmap --query runs, with its result table or outfile and its exit
//     status. Started with an explicit --interactive-query that client is
//     interactive dmap, whose session stays open (:query replaces the
//     query); its line editor starts with the builder's history
//     (runHistory), so Up recalls the query built.
//
// The query does not run on the builder's sessions because a session
// started with SESSION START never ends by itself (dserver keeps it for
// the next update), so the client could not tell when the result is
// complete; the plain command stream of the run ends with the server's
// close handshake, as it always did. The price is one more connection per
// server, which the auth-key fast reconnect keeps cheap.
type QueryBuilderClient struct {
	baseClient
	// runArgs are the arguments dmap was started with: those of the run,
	// once the built query and files are set on them. InteractiveQuery is
	// as given on the command line, so the run is interactive only when
	// it was asked for.
	runArgs config.Args
	// newRunner creates the client of the run for its arguments
	// (NewMaprClient) with the control lines its line editor starts its
	// history with, if it is interactive; tests replace it.
	newRunner func(args config.Args, history []string) (Client, error)
	// built is what the control loop handed over (setBuilt), guarded by
	// builtMu.
	builtMu sync.Mutex
	built   *builtQuery
}

// builtQuery is the outcome of the builder: the query entered and the
// files it reads (the --files of the command line, or of a :reload).
type builtQuery struct {
	files string
	query string
}

var _ Client = (*QueryBuilderClient)(nil)

// NewQueryBuilderClient returns the client of dmap started without a query
// where the query builder opens (cli.InteractiveDefault.ResolveQueryBuilder
// decides that). args are dmap's as parsed: without a query, and with
// InteractiveQuery telling whether the session stays open after the built
// query ran.
func NewQueryBuilderClient(args config.Args, cfg config.RuntimeConfig, loggers LoggerDependencies,
	colorizers ...*brush.Brush) (*QueryBuilderClient, error) {
	if strings.TrimSpace(args.QueryStr) != "" {
		return nil, errors.New("the query builder is for a client started without a query")
	}
	loggers = loggers.normalized()
	colorizer := firstColorizer(colorizers)
	args.Mode = omode.MapClient
	args.QueryStr = ""
	c := &QueryBuilderClient{runArgs: args}
	c.newRunner = func(runArgs config.Args, history []string) (Client, error) {
		runner, err := NewMaprClient(runArgs, cfg, DefaultMode, loggers, colorizer)
		if err != nil {
			return nil, err
		}
		runner.lineHistory = history
		return runner, nil
	}

	// The builder itself is always interactive (control loop, prober) and
	// prints no periodic connection statistics over the line being built.
	builderArgs := args
	builderArgs.QueryBuilder = true
	builderArgs.InteractiveQuery = true
	builderArgs.Quiet = true
	profile := clientHandlerProfile(loggers.Client, true)
	profile.queryBuilt = c.setBuilt
	base, err := newBaseClient(builderArgs, cfg, loggers, colorizer, profile)
	if err != nil {
		return nil, fmt.Errorf("initialize query builder: %w", err)
	}
	c.baseClient = base
	return c, nil
}

// Start runs the builder until a query was entered, the user left it
// (:quit, the end of the control TTY) or ctx ended, and then, with a
// query, the run (see QueryBuilderClient). It returns the run's exit
// status, which is that of dmap --query; without a run the status of the
// builder's connections, 0 unless one failed.
func (c *QueryBuilderClient) Start(ctx context.Context, statsCh <-chan string) int {
	status := c.baseClient.Start(ctx, statsCh)
	built, ok := c.takeBuilt()
	if !ok || ctx.Err() != nil {
		return status
	}
	runArgs := c.runArgs
	runArgs.What = built.files
	runArgs.QueryStr = built.query
	runner, err := c.newRunner(runArgs, runHistory(c.takeLineHistory()))
	if err != nil {
		c.clientLogger().Error("Unable to run the built query", err)
		_, _ = fmt.Fprintf(os.Stderr, "unable to run the built query: %v\n", err)
		return 1
	}
	return runner.Start(ctx, statsCh)
}

// runHistory turns the lines entered in the builder (most recent first)
// into the history of the interactive run that follows it: a control
// command is one there too, and a line that was a query, the last one
// being the query that runs, is recalled as the ":query" command that
// replaces the running query with it. So Up in the run brings back the
// query built, ready to be changed and entered.
func runHistory(builderLines []string) []string {
	history := make([]string, 0, len(builderLines))
	for _, line := range builderLines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, ":") {
			trimmed = queryCommand + " " + trimmed
		}
		history = append(history, trimmed)
	}
	return history
}

// setBuilt takes the query entered in the builder and the files it reads
// (clientProfile.queryBuilt, called by the control loop).
func (c *QueryBuilderClient) setBuilt(files, query string) {
	c.builtMu.Lock()
	defer c.builtMu.Unlock()
	c.built = &builtQuery{files: files, query: query}
}

// takeBuilt returns what the builder handed over, if anything.
func (c *QueryBuilderClient) takeBuilt() (builtQuery, bool) {
	c.builtMu.Lock()
	defer c.builtMu.Unlock()
	if c.built == nil {
		return builtQuery{}, false
	}
	return *c.built, true
}
