package clients

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// runnerFunc is a Client whose Start is the function.
type runnerFunc func(ctx context.Context) int

func (f runnerFunc) Start(ctx context.Context, _ <-chan string) int { return f(ctx) }

// TestNewQueryBuilderClient checks what the constructor sets up for the two
// phases: a builder that is interactive, retries and holds sessions without
// a workload although files were given, and run arguments that are dmap's
// as given (not interactive unless asked for, never the builder's).
func TestNewQueryBuilderClient(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		args := config.Args{Mode: omode.MapClient, What: "/var/log/app.log", Serverless: true,
			ConnectionsPerCPU: 1, InteractiveQuery: interactive}
		c, err := NewQueryBuilderClient(args, clientTestRuntimeConfig(), LoggerDependencies{})
		if err != nil {
			t.Fatalf("constructor error = %v", err)
		}
		if !isQueryBuilder(c.Args) || !c.InteractiveQuery || !c.profile.retry || c.profile.queryBuilt == nil {
			t.Fatalf("builder state = builder:%v interactive:%v retry:%v", isQueryBuilder(c.Args),
				c.InteractiveQuery, c.profile.retry)
		}
		if commands, err := c.sessionSpec.Commands(); err != nil || len(commands) != 0 ||
			c.sessionSpec.Mode != omode.TailClient {
			t.Fatalf("builder session spec = %+v (commands %q, %v), want one without workload",
				c.sessionSpec, commands, err)
		}
		if len(c.connections) != 1 || c.What != "/var/log/app.log" {
			t.Fatalf("connections = %d, files %q", len(c.connections), c.What)
		}
		if c.runArgs.QueryBuilder || c.runArgs.InteractiveQuery != interactive || c.runArgs.Quiet {
			t.Fatalf("run arguments = %+v, want dmap's as given", c.runArgs)
		}
	}
}

// TestNewQueryBuilderClientRejectsQuery: with a query there is nothing to
// build; dmap runs NewMaprClient then.
func TestNewQueryBuilderClientRejectsQuery(t *testing.T) {
	args := config.Args{Mode: omode.MapClient, What: "/var/log/app.log", Serverless: true,
		ConnectionsPerCPU: 1, QueryStr: "from STATS select count($line)"}
	if c, err := NewQueryBuilderClient(args, clientTestRuntimeConfig(), LoggerDependencies{}); err == nil {
		t.Fatalf("constructor accepted a query: %+v", c.Args)
	}
}

// TestNewMaprClientStillRejectsMissingQuery: outside the builder a missing
// query stays the error it was (jobs, dmap without a terminal).
func TestNewMaprClientStillRejectsMissingQuery(t *testing.T) {
	args := config.Args{Mode: omode.MapClient, What: "/var/log/app.log", Serverless: true, ConnectionsPerCPU: 1}
	_, err := NewMaprClient(args, clientTestRuntimeConfig(), DefaultMode, LoggerDependencies{})
	if err == nil || !strings.Contains(err.Error(), "no mapreduce query specified") {
		t.Fatalf("err = %v, want the missing query error", err)
	}
}

// newBuilderStartTestClient returns a query builder client whose builder
// phase is replaced by builder (it may hand a query over with setBuilt)
// and whose run records its arguments and returns runStatus.
func newBuilderStartTestClient(runStatus int) (*QueryBuilderClient, *[]config.Args) {
	var ran []config.Args
	c := &QueryBuilderClient{runArgs: config.Args{Mode: omode.MapClient, What: "/var/log/app.log"}}
	c.baseClient = baseClient{mu: newBaseClientMu(), loggers: LoggerDependencies{}.normalized()}
	c.stats = newTailStats(0, nil, 0, nil)
	c.newRunner = func(args config.Args, _ []string) (Client, error) {
		ran = append(ran, args)
		return runnerFunc(func(context.Context) int { return runStatus }), nil
	}
	return c, &ran
}

// TestQueryBuilderClientStartRunsBuiltQuery covers the hand-over from the
// builder to the run: a built query runs with dmap's arguments, the built
// query and the builder's files, and its status is the exit status; no
// query (:quit) and a canceled context run nothing; a run that can not be
// created exits 1.
func TestQueryBuilderClientStartRunsBuiltQuery(t *testing.T) {
	const query = "from STATS select count($line)"

	t.Run("built query runs", func(t *testing.T) {
		c, ran := newBuilderStartTestClient(2)
		c.runArgs.InteractiveQuery = true
		c.setBuilt("/var/log/other.log", query)
		if status := c.Start(context.Background(), nil); status != 2 {
			t.Fatalf("status = %d, want the run's 2", status)
		}
		if len(*ran) != 1 {
			t.Fatalf("runs = %d, want 1", len(*ran))
		}
		got := (*ran)[0]
		if got.QueryStr != query || got.What != "/var/log/other.log" || got.Mode != omode.MapClient ||
			!got.InteractiveQuery || got.QueryBuilder {
			t.Fatalf("run arguments = %+v", got)
		}
	})
	t.Run("no query runs nothing", func(t *testing.T) {
		c, ran := newBuilderStartTestClient(2)
		if status := c.Start(context.Background(), nil); status != 0 || len(*ran) != 0 {
			t.Fatalf("status = %d, runs = %d, want 0 and none", status, len(*ran))
		}
	})
	t.Run("canceled context runs nothing", func(t *testing.T) {
		c, ran := newBuilderStartTestClient(2)
		c.setBuilt("/var/log/app.log", query)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if status := c.Start(ctx, nil); status != 0 || len(*ran) != 0 {
			t.Fatalf("status = %d, runs = %d, want 0 and none", status, len(*ran))
		}
	})
	t.Run("run that can not be created", func(t *testing.T) {
		c, _ := newBuilderStartTestClient(0)
		c.newRunner = func(config.Args, []string) (Client, error) {
			return nil, errors.New("no server to connect to")
		}
		c.setBuilt("/var/log/app.log", query)
		if status := c.Start(context.Background(), nil); status != 1 {
			t.Fatalf("status = %d, want 1", status)
		}
	})
}

// TestRunHistory: the builder's lines become the history of the interactive
// run, most recent first: commands as they are, a query as the :query
// command that replaces the running query with it.
func TestRunHistory(t *testing.T) {
	got := runHistory([]string{"  from STATS select count($line) ", ":fields STATS",
		":reload --files /var/log/b.log", "from STATS select count("})
	want := []string{":query from STATS select count($line)", ":fields STATS",
		":reload --files /var/log/b.log", ":query from STATS select count("}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("run history = %q, want %q", got, want)
	}
	if got := runHistory(nil); len(got) != 0 {
		t.Fatalf("run history of no lines = %q", got)
	}
}

// TestRunnerStartsWithTheBuildersHistory: the client of the run is created
// with the history it was given, which its line editor starts with.
func TestRunnerStartsWithTheBuildersHistory(t *testing.T) {
	args := config.Args{Mode: omode.MapClient, What: "/var/log/app.log", Serverless: true, ConnectionsPerCPU: 1}
	c, err := NewQueryBuilderClient(args, clientTestRuntimeConfig(), LoggerDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	args.QueryStr = "from STATS select count($line)"
	history := []string{":query " + args.QueryStr}
	runner, err := c.newRunner(args, history)
	if err != nil {
		t.Fatal(err)
	}
	mapr, ok := runner.(*MaprClient)
	if !ok || strings.Join(mapr.seedHistory(), "|") != strings.Join(history, "|") {
		t.Fatalf("runner %T starts with the history %q, want %q", runner, mapr.seedHistory(), history)
	}
}
