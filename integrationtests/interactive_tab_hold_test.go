package integrationtests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Files and texts of the Tab hold tests.
const (
	// tabHoldDir is the directory the followed log lies in; its
	// subdirectory sub is listed by no one before the Tab.
	tabHoldDir = "tabhold.d"
	// tabHoldLine is a followed line: a STATS line of the default log
	// format with its number.
	tabHoldLine = "INFO|1002-071143|1|stats.go:56|8|13|7|0.21|471h0m21s|MAPREDUCE:STATS|tick=%d|conns=1"
	// tabHoldTick is in every followed line and in nothing the editor
	// prints (a field listing names tick without the bars).
	tabHoldTick = "|tick="
	// typingHintStart starts the hint the editor prints at the first key
	// of a line (interactiveTypingHint).
	typingHintStart = "(output paused while you type"
)

var tabHoldTickNumber = regexp.MustCompile(`\|tick=(\d+)\|`)

// Texts of the control loop (internal/clients/interactive_pause.go).
const (
	explicitPauseMessage = "output paused, press Enter to resume"
	outputResumedMessage = "output resumed"
	helpLastLine         = "interactive-query.md)."
	tableProbedMessage   = "table STATS probed on its own"
)

// tabHoldCase is one wait for a server's answer while the output is
// paused: explicit says whether an empty line paused it first (else the
// typing does), typed is the line, key what triggers the wait (Tab, or
// Enter for :fields TABLE) and answered what the terminal shows once the
// answer arrived.
type tabHoldCase struct {
	name     string
	explicit bool
	typed    string
	key      string
	answered string
}

// staysPaused reports whether the output is still paused once the case's
// line was entered, until an empty line: an explicit pause stays, and
// :fields keeps a typing pause.
func (c tabHoldCase) staysPaused() bool {
	return c.explicit || c.key == "\r"
}

// TestInteractiveTabHoldsOutput runs dtail on a PTY following a log that
// grows by 20 lines a second, against dserver and serverless, and waits
// for a server's answer while the output is paused, for typing or
// explicitly: Tab on a path listing of a directory not listed yet, Tab on
// a table probe after a typed from table, and :fields TABLE, which waits
// for a table probe and keeps the output paused. The answer is read by the
// handlers that write the output, so the output's writers go on during
// the wait, but none of the followed lines may reach the terminal between
// the typing and the resume of the output (the answer to the entered
// line, or the empty line that ends an explicit pause): they are held and
// printed after it, in order and complete.
func TestInteractiveTabHoldsOutput(t *testing.T) {
	skipIfNotIntegrationTest(t)
	query := ":query from STATS select "
	for _, tab := range []tabHoldCase{
		{"path listing", false, ":reload --files " + tabHoldDir + "/sub/", "\t", "xfile.log"},
		{"table probe", false, query, "\t", "aggregation:"},
		{"explicit pause, table probe", true, query, "\t", "aggregation:"},
		{"explicit pause, fields table", true, ":fields STATS", "\r", tableProbedMessage},
		{"fields table", false, ":fields STATS", "\r", tableProbedMessage},
	} {
		for _, serverless := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/serverless=%v", tab.name, serverless), func(t *testing.T) {
				testInteractiveTabHoldsOutput(t, serverless, tab)
			})
		}
	}
}

func testInteractiveTabHoldsOutput(t *testing.T, serverless bool, tab tabHoldCase) {
	testLogger := NewTestLogger("TestInteractiveTabHoldsOutput")
	defer writeLogFileIgnoringError(testLogger)
	ctx, cancel := createTestContextWithTimeout(t)
	ctx = WithTestLogger(ctx, testLogger)
	defer cancel()
	followed := writeTabHoldDir(ctx, t)

	argv := []string{"../dtail", "--cfg", "none", "--logger", "stdout", "--noColor",
		"--files", followed, "--interactive-query"}
	if !serverless {
		port, _ := startInteractiveTestServer(ctx, t)
		argv = append(argv, "--servers", fmt.Sprintf("localhost:%d", port), "--trustAllHosts")
	}
	output, err := runInteractivePTYCommand(ctx, argv, tab.steps())
	if err != nil {
		t.Fatalf("run interactive dtail: %v\noutput:\n%s", err, output)
	}
	if strings.Contains(output, "no listing of") || strings.Contains(output, "have not answered the probe") {
		t.Fatalf("the wait's answer did not arrive in time:\n%s", output)
	}
	from, to := tab.heldSpan(t, output)
	requireHeldBetween(t, output, from, to, tabHoldTick)
	if tab.staysPaused() {
		requireResumedByEnter(t, output, to)
	}
}

// requireResumedByEnter requires that the first followed line behind the
// offset answered (the answer to a line that leaves the output paused)
// comes only after the empty line that resumed the output was entered: the
// prompt drawn after the answer is followed by that line's line break. The
// resume prints the held lines before its "output resumed", so that
// message does not tell; a line printed while still paused would stand
// right behind the prompt.
func requireResumedByEnter(t *testing.T, output string, answered int) {
	t.Helper()
	rest := output[answered:]
	tick := strings.Index(rest, "REMOTE|")
	prompt := strings.LastIndex(rest[:max(tick, 0)], "dtail> ")
	if tick < 0 || prompt < 0 || !strings.Contains(rest[prompt:tick], "\n") {
		t.Fatalf("followed lines printed while the output was still paused after the answer:\n%s", rest)
	}
}

// steps are the keys of the case: the pause, the line, the key that waits
// for the server, the entered line and, where the output stays paused, the
// empty line that resumes it.
func (c tabHoldCase) steps() []interactiveStep {
	var steps []interactiveStep
	paused := typingHintStart
	if c.explicit {
		steps = append(steps, interactiveStep{WaitFor: tabHoldTick, Input: ""})
		paused = explicitPauseMessage
	}
	steps = append(steps,
		// Followed lines pile up behind the paused output before the key.
		interactiveStep{WaitFor: map[bool]string{true: paused, false: tabHoldTick}[c.explicit],
			Delay: 500 * time.Millisecond, Input: c.typed, Raw: true},
		interactiveStep{Delay: 500 * time.Millisecond, Input: c.key, Raw: true})
	if c.key == "\t" {
		// More arrive between the Tab's answer and the entered line.
		steps = append(steps, interactiveStep{WaitFor: c.answered, Delay: time.Second, Input: "\x15:show"})
		c.answered = "workload="
	}
	if c.staysPaused() {
		steps = append(steps,
			interactiveStep{WaitFor: c.answered, Delay: time.Second, Input: ""},
			interactiveStep{WaitFor: outputResumedMessage, Delay: time.Second, Input: ":quit"})
		return steps
	}
	return append(steps, interactiveStep{WaitFor: c.answered, Delay: time.Second, Input: ":quit"})
}

// heldSpan returns the offsets of output between which no followed line
// may be printed: from the typed line (in an explicit pause the one after
// the help, which holds command examples; else the typing hint) to the
// answer to the entered line, where a typing pause ends (an output that
// stays paused then is checked by requireResumedByEnter).
func (c tabHoldCase) heldSpan(t *testing.T, output string) (int, int) {
	t.Helper()
	from := strings.Index(output, typingHintStart)
	if c.explicit {
		from = indexAfter(output, explicitPauseMessage, helpLastLine, c.typed[:4])
	}
	answered := c.answered
	if c.key == "\t" {
		answered = "workload="
	}
	to := indexAfter(output[max(from, 0):], answered) + max(from, 0)
	if from < 0 || to < from {
		t.Fatalf("output lacks the typed line of %q followed by the resume:\n%s", c.name, output)
	}
	return from, to
}

// indexAfter returns the offset of the last of texts in s, each searched
// behind the one before it, or -1.
func indexAfter(s string, texts ...string) int {
	at := 0
	for _, text := range texts {
		i := strings.Index(s[at:], text)
		if i < 0 {
			return -1
		}
		at += i
	}
	return at
}

// requireHeldBetween requires that output holds no text with marker
// between the offsets from (the line being typed) and to (the resume of
// the output), that marked lines follow after to, and that the numbers of
// all followed lines of the output are consecutive: the lines held during
// the wait come after the resume, none lost, repeated or out of order.
func requireHeldBetween(t *testing.T, output string, from, to int, marker string) {
	t.Helper()
	if between := output[from:to]; strings.Contains(between, marker) {
		t.Fatalf("%d lines with %q reached the terminal between the typing and the resume:\n%s",
			strings.Count(between, marker), marker, between)
	}
	if !strings.Contains(output[to:], marker) {
		t.Fatalf("no line with %q after the resume: the held lines are lost:\n%s", marker, output[to:])
	}
	last := -1
	for _, match := range tabHoldTickNumber.FindAllStringSubmatch(output, -1) {
		n, _ := strconv.Atoi(match[1])
		if last >= 0 && n != last+1 {
			t.Fatalf("followed line %d after line %d: lost, repeated or out of order:\n%s", n, last, output)
		}
		last = n
	}
}

// writeTabHoldDir writes tabHoldDir with the followed log and a
// subdirectory, appends a numbered line to the log every 50 ms until the
// test ends, and returns the log's path.
func writeTabHoldDir(ctx context.Context, t *testing.T) string {
	t.Helper()
	if err := os.RemoveAll(tabHoldDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tabHoldDir) })
	followed := filepath.Join(tabHoldDir, "follow.log")
	for path, content := range map[string]string{
		followed: fmt.Sprintf(tabHoldLine, 0) + "\n",
		filepath.Join(tabHoldDir, "sub", "xfile.log"): "x\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	appendNumberedLines(ctx, t, followed, tabHoldLine)
	return followed
}

// appendNumberedLines appends format with the numbers from 1 on to path,
// one line every 50 ms, until the test ends. Its cleanup stops the appends
// before the cleanups registered earlier remove the file.
func appendNumberedLines(ctx context.Context, t *testing.T, path, format string) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	go func() {
		defer close(done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for i := 1; ; i++ {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if err := appendLines(path, fmt.Sprintf(format, i)); err != nil {
				t.Errorf("append to %s: %v", path, err)
				return
			}
		}
	}()
}

// builderLogLine matches a log line of the client or, in serverless mode,
// of its in-process server, as the stdout logger prints it.
var builderLogLine = regexp.MustCompile(`(CLIENT|SERVER)\|[^|\r\n]*\|(DEBUG|INFO|WARN|ERROR)\|`)

// TestDMapQueryBuilderTabHoldsLogLines runs the query builder of dmap on a
// PTY, against dserver and serverless, with debug logging, and waits for a
// table probe's answer while a line is edited: Tab after a typed "from
// STATS ", and :fields STATS. The builder has no payload, but the log
// lines of that probe (the client's "Table probe answered", in serverless
// mode also the in-process server's) are written during the wait: they
// must not reach the terminal between the typing and the answer to the
// entered line, and are printed after it.
func TestDMapQueryBuilderTabHoldsLogLines(t *testing.T) {
	skipIfNotIntegrationTest(t)
	const rejected = "query builder: invalid query"
	for _, wait := range []struct{ name, typed, entered string }{
		{"tab", "from STATS ", rejected},
		{"fields table", ":fields STATS", tableProbedMessage},
	} {
		for _, serverless := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/serverless=%v", wait.name, serverless), func(t *testing.T) {
				testBuilderHoldsLogLines(t, serverless, wait.typed, wait.entered)
			})
		}
	}
}

func testBuilderHoldsLogLines(t *testing.T, serverless bool, typed, entered string) {
	testLogger := NewTestLogger("TestDMapQueryBuilderTabHoldsLogLines")
	defer writeLogFileIgnoringError(testLogger)
	ctx, cancel := createTestContextWithTimeout(t)
	ctx = WithTestLogger(ctx, testLogger)
	defer cancel()
	writeBuilderLog(t)
	argv, _ := builderArgv(ctx, t, serverless)

	steps := []interactiveStep{
		{WaitFor: probeStoredMessage, Delay: 500 * time.Millisecond, Input: typed, Raw: true}}
	if !strings.HasPrefix(typed, ":") {
		// Tab inserts the next step, select; the query is not finished, so
		// Enter is answered and the builder stays.
		steps = append(steps,
			interactiveStep{WaitFor: typed, Delay: 200 * time.Millisecond, Input: "\t", Raw: true},
			interactiveStep{WaitFor: "select ", Delay: time.Second, Input: "\r", Raw: true})
	} else {
		steps = append(steps, interactiveStep{WaitFor: typed, Delay: 200 * time.Millisecond, Input: "\r", Raw: true})
	}
	steps = append(steps,
		// The answer first: a :quit right behind the Enter would be taken
		// for pasted text and stay on the line.
		interactiveStep{WaitFor: entered, Input: "", Raw: true},
		interactiveStep{WaitFor: tableProbeAnsweredMessage, Delay: 300 * time.Millisecond, Input: ":quit"})
	output, err := runInteractivePTYCommand(ctx, argv, steps)
	if err != nil {
		t.Fatalf("run dmap: %v\noutput:\n%s", err, output)
	}
	start := indexAfter(output, probeStoredMessage, typed)
	end := strings.Index(output, entered)
	if start < 0 || end < start {
		t.Fatalf("output lacks %q after the typed %q:\n%s", entered, typed, output)
	}
	if lines := builderLogLine.FindAllString(output[start:end], -1); len(lines) > 0 {
		t.Fatalf("%d log lines reached the terminal between the typing and the entered line:\n%s",
			len(lines), output[start:end])
	}
	if !strings.Contains(output[end:], tableProbeAnsweredMessage) {
		t.Fatalf("the log line held during the wait is lost:\n%s", output[end:])
	}
}

// tableProbeAnsweredMessage is the debug line the client logs when a
// server answered a table probe (interactiveProber.probeTable).
const tableProbeAnsweredMessage = "Table probe answered"
