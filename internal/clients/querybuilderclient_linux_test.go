//go:build linux

package clients

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// startBuilderOnPTY starts a query builder client reading a log file on a new
// pseudo terminal and waits for its first prompt.
func startBuilderOnPTY(t *testing.T) (master *os.File, ttyPath string, ran chan builderRun, done chan int) {
	t.Helper()
	master, ttyPath = openTestPTY(t)
	c, ran := newBuilderPTYClient(ttyPath, "/var/log/app.log")
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
			master, _, ran, done := startBuilderOnPTY(t)
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
	master, _, ran, done := startBuilderOnPTY(t)

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

// nextTTYReader opens the terminal at ttyPath as whoever reads it after
// dmap (the shell) would.
func nextTTYReader(t *testing.T, ttyPath string) *os.File {
	t.Helper()
	reader, err := os.OpenFile(ttyPath, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open the terminal as its next reader: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}

// requireNoTTYInput fails if the terminal holds input for its next reader.
func requireNoTTYInput(t *testing.T, reader *os.File) {
	t.Helper()
	buf := make([]byte, 4096)
	_ = reader.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, err := reader.Read(buf)
	if n != 0 || (!errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, syscall.EAGAIN)) {
		t.Fatalf("input left for the next reader of the terminal: %d bytes %q (err %v)", n, buf[:min(n, 80)], err)
	}
}

// pasteUnbracketed writes text to the terminal from a goroutine, as a
// terminal without bracketed paste delivers a paste (the write blocks while
// the terminal's input queue is full), reads the terminal's output
// meanwhile and returns it once all of text was written and taken.
func pasteUnbracketed(t *testing.T, master *os.File, text string) string {
	t.Helper()
	written := make(chan error, 1)
	go func() {
		_, err := master.WriteString(text)
		written <- err
	}()
	defer func() { _ = master.SetReadDeadline(time.Time{}) }()
	var output strings.Builder
	buf := make([]byte, 4096)
	deadline := time.Now().Add(20 * time.Second)
	for quiet := 0; quiet < 4; {
		if time.Now().After(deadline) {
			t.Fatalf("the paste of %d bytes was not taken within 20s", len(text))
		}
		select {
		case err := <-written:
			if err != nil {
				t.Fatalf("paste: %v", err)
			}
			written = nil
		default:
		}
		_ = master.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _ := master.Read(buf)
		output.Write(buf[:n])
		// Done once the write returned and the terminal printed nothing
		// for 400 ms: the editor then ended the paste.
		if quiet++; n > 0 || written != nil {
			quiet = 0
		}
	}
	return output.String()
}

// unbracketedPaste returns a valid query line followed by shell command
// lines, size bytes in all.
func unbracketedPaste(size int) string {
	var paste strings.Builder
	paste.WriteString("from STATS select count($line)\n")
	for i := 0; paste.Len() < size; i++ {
		fmt.Fprintf(&paste, "echo LEAKED_%05d\n", i)
	}
	return paste.String()
}

// TestQueryBuilderUnbracketedPasteOnPTY pastes, without paste markers (a
// terminal without bracketed paste), a valid query line followed by shell
// command lines: 3 KB, which the terminal's input queue holds at once, and
// 16 to 64 KB, which arrive in pieces while the editor reads. No line of
// it is entered: nothing runs, the text stays on the line as one line,
// and after :quit, or after a typed Enter (the line is then too long or
// no query) and :quit, nothing of it is left for the next reader of the
// terminal. Entered line by line, the query line ran and ended dmap, and
// the shell read and ran the lines behind it.
func TestQueryBuilderUnbracketedPasteOnPTY(t *testing.T) {
	t.Parallel()
	for _, size := range []int{3 << 10, 16 << 10, 28 << 10, 64 << 10} {
		for _, enter := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d bytes, typed Enter %v", size, enter), func(t *testing.T) {
				t.Parallel()
				master, ttyPath, ran, done := startBuilderOnPTY(t)
				reader := nextTTYReader(t, ttyPath)
				out := pasteUnbracketed(t, master, unbracketedPaste(size))
				requireNoRun(t, ran, "after the paste")
				if strings.Contains(out, builderErrorPrefix) || strings.Contains(out, "running the query") {
					t.Fatalf("a pasted line was entered: %q", out[:min(len(out), 400)])
				}
				if enter {
					writePTY(t, master, "\n")
					waitForPTYOutput(t, master, "\r\n"+builderPrompt, done)
					requireNoRun(t, ran, "after Enter on the pasted text")
				} else {
					writePTY(t, master, "\x15")
				}
				// Read on: erasing a line of 4095 characters writes more
				// than the terminal's output queue holds.
				writePTY(t, master, ":quit\n")
				waitForPTYOutput(t, master, "quitting interactive session", done)
				awaitBuilderEnd(t, done, 0)
				requireNoRun(t, ran, "after :quit")
				requireNoTTYInput(t, reader)
			})
		}
	}
}

// TestQueryBuilderDiscardsInputInFlightOnPTY: text that arrives while the
// builder ends (typed or pasted after the Enter that ran the query, past
// the quiet time that makes it an Enter) is discarded until the terminal
// was quiet, also what arrives after the first flush of its input queue.
func TestQueryBuilderDiscardsInputInFlightOnPTY(t *testing.T) {
	t.Parallel()
	master, ttyPath, ran, done := startBuilderOnPTY(t)
	reader := nextTTYReader(t, ttyPath)
	writePTY(t, master, "from STATS select count($line)\n")
	waitForPTYOutput(t, master, "running the query", done)
	// Behind the Enter by more than the quiet time, in pieces 30 ms apart.
	for i := 0; i < 5; i++ {
		writePTY(t, master, "touch /tmp/would-run\n")
		time.Sleep(30 * time.Millisecond)
	}
	awaitBuilderEnd(t, done, 3)
	if run := <-ran; run.args.QueryStr != "from STATS select count($line)" {
		t.Fatalf("query run = %q", run.args.QueryStr)
	}
	requireNoTTYInput(t, reader)
}

// TestQueryBuilderTypedEnterAfterQuietOnPTY: a line typed with its Enter in
// one go (typed ahead) is entered, and what is typed after the quiet time
// is the next line.
func TestQueryBuilderTypedEnterAfterQuietOnPTY(t *testing.T) {
	t.Parallel()
	master, _, ran, done := startBuilderOnPTY(t)
	writePTY(t, master, "nonsense\n")
	time.Sleep(100 * time.Millisecond)
	writePTY(t, master, "from STATS select count($line)\n")
	out := waitForPTYOutput(t, master, "running the query", done)
	if !strings.Contains(out, builderErrorPrefix) {
		t.Fatalf("the first line was not entered on its own: %q", out)
	}
	awaitBuilderEnd(t, done, 3)
	if run := <-ran; run.args.QueryStr != "from STATS select count($line)" {
		t.Fatalf("query run = %q", run.args.QueryStr)
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
	// Stdout is the control terminal, through a descriptor of its own.
	deps.logOnTTY = func(tty *os.File) bool { return sameTerminal(slave, tty) }
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

// TestBuilderLogStaysOffTheEditor: log lines that do not go to the control
// TTY (stdout redirected to a file, or on another terminal than the control
// TTY, as with --control-tty) are not passed through the editor, and
// outside the builder they never are.
func TestBuilderLogStaysOffTheEditor(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(*testing.T, *QueryBuilderClient, *editorDeps){
		"stdout is a file": func(t *testing.T, _ *QueryBuilderClient, deps *editorDeps) {
			stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stdout.Close() })
			deps.logOnTTY = func(tty *os.File) bool { return sameTerminal(stdout, tty) }
		},
		"stdout is another terminal": func(t *testing.T, _ *QueryBuilderClient, deps *editorDeps) {
			_, otherPath := openTestPTY(t)
			stdout, err := os.OpenFile(otherPath, os.O_RDWR|syscall.O_NOCTTY, 0)
			if err != nil {
				t.Skipf("pseudo terminal slave: %v", err)
			}
			t.Cleanup(func() { _ = stdout.Close() })
			deps.logOnTTY = func(tty *os.File) bool { return sameTerminal(stdout, tty) }
		},
		"no builder": func(_ *testing.T, c *QueryBuilderClient, deps *editorDeps) {
			deps.logOnTTY = func(*os.File) bool { return true }
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
			setup(t, c, &deps)
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

// TestSameTerminalControllingTTY: the default control TTY is /dev/tty, an
// alias of the controlling terminal with a device of its own, so stdout on
// that terminal is another file and still the same terminal; stdout on
// another terminal than the controlling one is not. It runs a child of
// this test binary in a session of its own with a pseudo terminal as its
// controlling terminal (sameTerminalChild).
func TestSameTerminalControllingTTY(t *testing.T) {
	t.Parallel()
	_, ctlPath := openTestPTY(t)
	_, otherPath := openTestPTY(t)
	for name, tt := range map[string]struct {
		stdout string
		want   string
	}{
		"stdout is the controlling terminal": {stdout: ctlPath, want: "true"},
		"stdout is another terminal":         {stdout: otherPath, want: "false"},
	} {
		t.Run(name, func(t *testing.T) {
			ctl, err := os.OpenFile(ctlPath, os.O_RDWR|syscall.O_NOCTTY, 0)
			if err != nil {
				t.Skipf("pseudo terminal slave: %v", err)
			}
			defer func() { _ = ctl.Close() }()
			stdout, err := os.OpenFile(tt.stdout, os.O_RDWR|syscall.O_NOCTTY, 0)
			if err != nil {
				t.Skipf("pseudo terminal slave: %v", err)
			}
			defer func() { _ = stdout.Close() }()
			result, report, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = result.Close() }()
			cmd := exec.Command(os.Args[0], "-test.run=^TestSameTerminalChild$")
			cmd.Env = append(os.Environ(), sameTerminalChildEnv+"=1")
			cmd.Stdin, cmd.Stdout, cmd.ExtraFiles = ctl, stdout, []*os.File{report}
			// The child's stdin (descriptor 0) becomes its controlling terminal.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := cmd.Run(); err != nil {
				t.Fatalf("child: %v", err)
			}
			_ = report.Close()
			got, _ := io.ReadAll(result)
			if string(got) != tt.want {
				t.Fatalf("sameTerminal(stdout, /dev/tty) in the child = %q, want %s", got, tt.want)
			}
		})
	}
}

// sameTerminalChildEnv marks the child process of
// TestSameTerminalControllingTTY.
const sameTerminalChildEnv = "DTAIL_TEST_SAME_TERMINAL_CHILD"

// TestSameTerminalChild is the child of TestSameTerminalControllingTTY: it
// reports on descriptor 3 whether its stdout is the terminal /dev/tty
// names. Run as a test of its own it does nothing.
func TestSameTerminalChild(t *testing.T) {
	if os.Getenv(sameTerminalChildEnv) == "" {
		return
	}
	report := os.NewFile(3, "report")
	defer func() { _ = report.Close() }()
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		_, _ = fmt.Fprintf(report, "open /dev/tty: %v", err)
		return
	}
	defer func() { _ = tty.Close() }()
	_, _ = fmt.Fprint(report, sameTerminal(os.Stdout, tty))
}

// TestSameTerminal: two descriptors of one terminal are the same terminal,
// two terminals are not, and a file is no terminal.
func TestSameTerminal(t *testing.T) {
	t.Parallel()
	open := func(path string) *os.File {
		file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
		if err != nil {
			t.Skipf("open %s: %v", path, err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	_, pathA := openTestPTY(t)
	_, pathB := openTestPTY(t)
	regular := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a1, a2, b, file := open(pathA), open(pathA), open(pathB), open(regular)
	for name, tt := range map[string]struct {
		a, b *os.File
		want bool
	}{
		"one terminal, two descriptors": {a1, a2, true},
		"two terminals":                 {a1, b, false},
		"a file and a terminal":         {file, a1, false},
		"a terminal and a file":         {a1, file, false},
		"one file twice":                {file, file, false},
	} {
		if got := sameTerminal(tt.a, tt.b); got != tt.want {
			t.Errorf("%s: sameTerminal = %v, want %v", name, got, tt.want)
		}
	}
}
