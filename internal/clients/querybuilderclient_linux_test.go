//go:build linux

package clients

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// newBuilderPTYClient returns a query builder client with ttyPath as its
// control TTY, one connection whose session runs until it is ended, and a
// run that records its arguments and returns status 3.
func newBuilderPTYClient(ttyPath, files string) (*QueryBuilderClient, chan config.Args) {
	ran := make(chan config.Args, 1)
	c := &QueryBuilderClient{runArgs: config.Args{Mode: omode.MapClient, What: files, ControlTTYPath: ttyPath}}
	args := c.runArgs
	args.QueryBuilder = true
	args.InteractiveQuery = true
	c.baseClient = baseClient{mu: newBaseClientMu(), Args: args, sessionSpec: startSessionSpec(args),
		loggers: LoggerDependencies{}.normalized(), profile: clientProfile{queryBuilt: c.setBuilt}}
	c.loggers.Client = newPausingTestLogger()
	c.stats = newTailStats(1, nil, 0, nil)
	c.connections = []connectors.Connector{&finalTestConnector{
		retryTestConnector: retryTestConnector{server: "srv1",
			handler: &statusTestHandler{outcome: handlers.SessionOutcome{}}},
		run: func(ctx context.Context) { <-ctx.Done() },
	}}
	c.newRunner = func(runArgs config.Args) (Client, error) {
		ran <- runArgs
		return runnerFunc(func(context.Context) int { return 3 }), nil
	}
	return c, ran
}

// awaitBuilderEnd waits for the builder client's Start to return want.
func awaitBuilderEnd(t *testing.T, done <-chan int, want int) {
	t.Helper()
	select {
	case status := <-done:
		if status != want {
			t.Fatalf("Start status = %d, want %d", status, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the query builder client did not end")
	}
}

// TestQueryBuilderOnPTY drives the query builder on a real pseudo terminal
// with the line editor: the banner says what to do, every line has the
// builder's prompt and typing shows no pause hint, Tab on the empty line
// starts the query with from (the completer of :query), a query that does
// not parse is answered and keeps the builder open, and Enter on one that
// parses ends the builder and starts the run with it, whose status is the
// exit status.
func TestQueryBuilderOnPTY(t *testing.T) {
	t.Parallel()
	master, ttyPath := openTestPTY(t)
	c, ran := newBuilderPTYClient(ttyPath, "/var/log/app.log")
	done := make(chan int, 1)
	go func() { done <- c.Start(context.Background(), nil) }()

	// The prompt follows the banner: nothing streams in the builder.
	waitForPTYOutput(t, master, builderBanner+"\r\r\n"+builderPrompt, done)
	writePTY(t, master, "\t")
	// No typing hint between the prompt and what the Tab drew.
	out := waitForPTYOutput(t, master, "from ", done)
	writePTY(t, master, "STATS select count(\n")
	// The rejected line is answered and followed by a new prompt (one wait
	// for both: the helper drops what it read past a match).
	out += waitForPTYOutput(t, master, "aggregation: count(\r\r\n"+builderPrompt, done)
	if !strings.Contains(out, builderErrorPrefix+"invalid query") {
		t.Fatalf("the rejected query was not answered as one: %q", out)
	}
	if strings.Contains(out, interactiveTypingHint) || strings.Contains(out, interactivePausedMessage) {
		t.Fatalf("the builder announced an output pause: %q", out)
	}
	select {
	case args := <-ran:
		t.Fatalf("a rejected query ran: %+v", args)
	default:
	}

	writePTY(t, master, "from STATS select count($line)\n")
	waitForPTYOutput(t, master, "running the query", done)
	awaitBuilderEnd(t, done, 3)
	args := <-ran
	if args.QueryStr != "from STATS select count($line)" || args.What != "/var/log/app.log" ||
		args.InteractiveQuery || args.QueryBuilder {
		t.Fatalf("run arguments = %+v", args)
	}
}

// TestQueryBuilderQuitOnPTY leaves the builder with :quit, and by closing
// the terminal: nothing runs and the status is 0.
func TestQueryBuilderQuitOnPTY(t *testing.T) {
	t.Parallel()
	for name, leave := range map[string]func(*testing.T, *os.File){
		":quit":           func(t *testing.T, master *os.File) { writePTY(t, master, ":quit\n") },
		"closed terminal": func(_ *testing.T, master *os.File) { _ = master.Close() },
	} {
		t.Run(name, func(t *testing.T) {
			master, ttyPath := openTestPTY(t)
			c, ran := newBuilderPTYClient(ttyPath, "/var/log/app.log")
			done := make(chan int, 1)
			go func() { done <- c.Start(context.Background(), nil) }()
			waitForPTYOutput(t, master, builderPrompt, done)
			leave(t, master)
			awaitBuilderEnd(t, done, 0)
			select {
			case args := <-ran:
				t.Fatalf("a query ran after leaving the builder: %+v", args)
			default:
			}
		})
	}
}

// TestQueryBuilderNoFilesOnPTY starts the builder without files: the banner
// is followed by the hint how to name files, a query is not run for the
// lack of them, and after :reload --files it runs with those files.
func TestQueryBuilderNoFilesOnPTY(t *testing.T) {
	t.Parallel()
	master, ttyPath := openTestPTY(t)
	c, ran := newBuilderPTYClient(ttyPath, "")
	c.prober = nil // Start sets it.
	done := make(chan int, 1)
	go func() { done <- c.Start(context.Background(), nil) }()

	hint := sessionNoFilesText + sessionNoFilesRemoteTab
	waitForPTYOutput(t, master, builderBanner+"\r\r\n"+hint, done)
	writePTY(t, master, "from STATS select count($line)\n")
	waitForPTYOutput(t, master, builderErrorPrefix+"query not run, "+hint, done)
	writePTY(t, master, ":reload --files /var/log/b.log\n")
	waitForPTYOutput(t, master, "files set", done)
	writePTY(t, master, "from STATS select count($line)\n")
	awaitBuilderEnd(t, done, 3)
	if args := <-ran; args.What != "/var/log/b.log" {
		t.Fatalf("run files = %q, want those of the :reload", args.What)
	}
}
