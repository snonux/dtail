//go:build linux

package clients

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// builderRun is what the query builder handed to the run.
type builderRun struct {
	args    config.Args
	history []string
}

// newBuilderPTYClient returns a query builder client with ttyPath as its
// control TTY, one connection whose session runs until it is ended, and a
// run that records what it was given and returns status 3.
func newBuilderPTYClient(ttyPath, files string) (*QueryBuilderClient, chan builderRun) {
	ran := make(chan builderRun, 1)
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
	c.newRunner = func(runArgs config.Args, history []string) (Client, error) {
		ran <- builderRun{args: runArgs, history: history}
		return runnerFunc(func(context.Context) int { return 3 }), nil
	}
	return c, ran
}

// startBuilderOnPTY starts a query builder client reading files on a new
// pseudo terminal and waits for its first prompt.
func startBuilderOnPTY(t *testing.T, files string) (master *os.File, ttyPath string, ran chan builderRun, done chan int) {
	t.Helper()
	master, ttyPath = openTestPTY(t)
	c, ran := newBuilderPTYClient(ttyPath, files)
	done = make(chan int, 1)
	go func() { done <- c.Start(context.Background(), nil) }()
	waitForPTYOutput(t, master, builderPrompt, done)
	return master, ttyPath, ran, done
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

// requireNoRun fails if the builder started a run.
func requireNoRun(t *testing.T, ran <-chan builderRun, when string) {
	t.Helper()
	select {
	case run := <-ran:
		t.Fatalf("%s: a query ran: %+v", when, run.args)
	default:
	}
}

// TestQueryBuilderOnPTY drives the query builder on a real pseudo terminal
// with the line editor: the banner says what to do, every line has the
// builder's prompt and typing shows no pause hint, Tab on the empty line
// starts the query with from (the completer of :query), a query that does
// not parse is answered and keeps the builder open, and Enter on one that
// parses ends the builder and starts the run with it, whose status is the
// exit status. The run gets the builder's lines as its history, the query
// as the :query command that would replace it.
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
	requireNoRun(t, ran, "after a rejected query")

	writePTY(t, master, "from STATS select count($line)\n")
	waitForPTYOutput(t, master, "running the query", done)
	awaitBuilderEnd(t, done, 3)
	run := <-ran
	if run.args.QueryStr != "from STATS select count($line)" || run.args.What != "/var/log/app.log" ||
		run.args.InteractiveQuery || run.args.QueryBuilder {
		t.Fatalf("run arguments = %+v", run.args)
	}
	want := []string{":query from STATS select count($line)", ":query from STATS select count("}
	if strings.Join(run.history, "|") != strings.Join(want, "|") {
		t.Fatalf("run history = %q, want %q", run.history, want)
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
			master, _, ran, done := startBuilderOnPTY(t, "/var/log/app.log")
			leave(t, master)
			awaitBuilderEnd(t, done, 0)
			requireNoRun(t, ran, "after leaving the builder")
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
	if run := <-ran; run.args.What != "/var/log/b.log" {
		t.Fatalf("run files = %q, want those of the :reload", run.args.What)
	}
}

// TestQueryBuilderPastedLinesAreOneLineOnPTY pastes a query of three lines
// (bracketed paste): it is one line on the edit line, with blanks for the
// line breaks, and nothing runs until Enter is typed. Entered line by
// line, as the editor does outside the builder, the first line would be
// rejected and the second, a valid query of its own, would run and end
// dmap with a result for another query than the one pasted.
func TestQueryBuilderPastedLinesAreOneLineOnPTY(t *testing.T) {
	t.Parallel()
	master, _, ran, done := startBuilderOnPTY(t, "/var/log/app.log")

	writePTY(t, master, "\x1b[200~from STATS\nselect count($line)\r\ngroup by $hostname\n\x1b[201~")
	out := waitForPTYOutput(t, master, "group by $hostname", done)
	// What an entered line would be answered with never appears.
	time.Sleep(300 * time.Millisecond)
	requireNoRun(t, ran, "after the paste, before Enter")
	if strings.Contains(out, builderErrorPrefix) || strings.Contains(out, "running the query") {
		t.Fatalf("a pasted line was entered: %q", out)
	}

	writePTY(t, master, "\n")
	out = waitForPTYOutput(t, master, "running the query", done)
	if strings.Contains(out, builderErrorPrefix) {
		t.Fatalf("the pasted query was rejected: %q", out)
	}
	awaitBuilderEnd(t, done, 3)
	// The CR LF of the paste is two blanks, which the query does not mind.
	if run := <-ran; strings.Join(strings.Fields(run.args.QueryStr), " ") !=
		"from STATS select count($line) group by $hostname" {
		t.Fatalf("query run = %q, want the three pasted lines as one", run.args.QueryStr)
	}
}

// TestQueryBuilderDiscardsPendingInputOnPTY types (no bracketed paste, as on
// a terminal without it) a query followed by many more lines at once: the
// first line runs and ends the builder, and the lines behind it, still in
// the terminal's input queue, are discarded. Left there, the shell that
// started dmap would read and run them once dmap exited.
func TestQueryBuilderDiscardsPendingInputOnPTY(t *testing.T) {
	t.Parallel()
	master, ttyPath, ran, done := startBuilderOnPTY(t, "/var/log/app.log")
	reader, err := os.OpenFile(ttyPath, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open the terminal as its next reader: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	// More than the editor reads at once, so most of it stays queued.
	typed := "from STATS select count($line)\n" + strings.Repeat("touch /tmp/would-run\n", 150)
	writePTY(t, master, typed)
	awaitBuilderEnd(t, done, 3)
	if run := <-ran; run.args.QueryStr != "from STATS select count($line)" {
		t.Fatalf("query run = %q", run.args.QueryStr)
	}
	// The terminal is in its line mode again: queued lines would be read.
	buf := make([]byte, 4096)
	_ = reader.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, err := reader.Read(buf)
	if n != 0 || (!errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, syscall.EAGAIN)) {
		t.Fatalf("input left for the next reader of the terminal: %q (err %v)", buf[:n], err)
	}
}

// redirectTestLogger is a client logger whose terminal output can be
// redirected (clientlog.Redirect): it records the writer it was given.
type redirectTestLogger struct {
	*pausingTestLogger
	mu        sync.Mutex
	out       io.Writer
	redirects int
}

func (l *redirectTestLogger) RedirectOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out = w
	l.redirects++
}

func (l *redirectTestLogger) output() (io.Writer, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out, l.redirects
}

// TestQueryBuilderLogLinesRedrawPromptOnPTY: while the builder edits, the
// client's log lines go through the line editor, so one arriving while the
// prompt waits is printed above it and prompt and line are drawn again;
// when the builder ends the logger has its own output back. Written
// straight to the terminal, the line landed on the prompt's row.
func TestQueryBuilderLogLinesRedrawPromptOnPTY(t *testing.T) {
	t.Parallel()
	master, ttyPath := openTestPTY(t)
	slave, err := os.OpenFile(ttyPath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudo terminal slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	c, _ := newBuilderPTYClient(ttyPath, "/var/log/app.log")
	logger := &redirectTestLogger{pausingTestLogger: newPausingTestLogger()}
	c.loggers.Client = logger
	deps := testEditorDeps()
	deps.logOnTerminal = func() bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		_ = c.runInteractiveControl(ctx, cancel, slave, nil, deps)
		done <- 0
	}()
	waitForPTYOutput(t, master, builderPrompt, done)
	out, _ := logger.output()
	if out == nil {
		t.Fatal("the logger's output was not passed through the editor")
	}

	writePTY(t, master, "from ST")
	waitForPTYOutput(t, master, "from ST", done)
	if _, err := io.WriteString(out, "SERVER|srv1|ERROR|failed to dial\n"); err != nil {
		t.Fatal(err)
	}
	// The log line, then the prompt and the line being edited again.
	waitForPTYOutput(t, master, "failed to dial\r\r\n"+builderPrompt+"from ST", done)

	writePTY(t, master, "\x15:quit\n")
	awaitBuilderEnd(t, done, 0)
	// Redirected once, and given back by every close of the control lines.
	if out, redirects := logger.output(); out != nil || redirects < 2 {
		t.Fatalf("after the builder: logger output %v after %d redirects, want its own back", out, redirects)
	}
}

// TestBuilderLogStaysOffTheEditor: log lines that do not go to the terminal
// (stdout redirected) are not passed through the editor, and outside the
// builder they never are.
func TestBuilderLogStaysOffTheEditor(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(*QueryBuilderClient, *editorDeps){
		"stdout is no terminal": func(_ *QueryBuilderClient, deps *editorDeps) {
			deps.logOnTerminal = func() bool { return false }
		},
		"no builder": func(c *QueryBuilderClient, deps *editorDeps) {
			deps.logOnTerminal = func() bool { return true }
			c.QueryBuilder = false
			c.Mode = omode.GrepClient
		},
	} {
		t.Run(name, func(t *testing.T) {
			master, ttyPath := openTestPTY(t)
			slave, err := os.OpenFile(ttyPath, os.O_RDWR|syscall.O_NOCTTY, 0)
			if err != nil {
				t.Skipf("pseudo terminal slave: %v", err)
			}
			t.Cleanup(func() { _ = slave.Close() })
			c, _ := newBuilderPTYClient(ttyPath, "/var/log/app.log")
			logger := &redirectTestLogger{pausingTestLogger: newPausingTestLogger()}
			c.loggers.Client = logger
			deps := testEditorDeps()
			setup(c, &deps)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan int, 1)
			go func() {
				_ = c.runInteractiveControl(ctx, cancel, slave, nil, deps)
				done <- 0
			}()
			waitForPTYOutput(t, master, "Tab", done)
			writePTY(t, master, ":quit\n")
			awaitBuilderEnd(t, done, 0)
			if _, redirects := logger.output(); redirects != 0 {
				t.Fatalf("the logger was redirected %d times", redirects)
			}
		})
	}
}
