package integrationtests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// controlWait bounds the wait for one response or event of a control
// channel.
const controlWait = 30 * time.Second

// controlProcess is a client run with --control json: requests go to its
// stdin, one JSON line each, responses and events come from its stderr, and
// its stdout is the data.
type controlProcess struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan map[string]any
	nextID int

	mu     sync.Mutex
	stdout bytes.Buffer
	// events are the lines that were no response, in order.
	events []map[string]any
}

// startControlProcess starts argv with the control channel on its standard
// streams and waits for its ready event.
func startControlProcess(ctx context.Context, t *testing.T, argv []string) *controlProcess {
	t.Helper()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &controlProcess{t: t, cmd: cmd, stdin: stdin, lines: make(chan map[string]any, 64)}
	cmd.Stdout = lockedWriter{mu: &p.mu, buf: &p.stdout}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", argv, err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	go p.readLines(stderr)
	if ready := p.event("ready"); ready["protocol"] != float64(1) {
		t.Fatalf("ready event: %v", ready)
	}
	return p
}

// lockedWriter appends to buf under mu.
type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// readLines decodes the lines of the control output; a line that is no JSON
// object fails the test, as the channel carries nothing else.
func (p *controlProcess) readLines(stderr io.Reader) {
	defer close(p.lines)
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			p.t.Errorf("control output line is no JSON object: %q", scanner.Text())
			continue
		}
		p.lines <- line
	}
}

// next returns the next line of the control output.
func (p *controlProcess) next() map[string]any {
	p.t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok {
			p.t.Fatalf("the control output ended; stdout:\n%s", p.data())
		}
		return line
	case <-time.After(controlWait):
		p.t.Fatalf("no control output within %s; stdout:\n%s", controlWait, p.data())
	}
	return nil
}

// event waits for the next event named name, keeping other events.
func (p *controlProcess) event(name string) map[string]any {
	p.t.Helper()
	for {
		line := p.next()
		if line["event"] == name {
			return line
		}
		if _, isResponse := line["ok"]; isResponse {
			p.t.Fatalf("response while waiting for the %s event: %v", name, line)
		}
		p.events = append(p.events, line)
	}
}

// request sends a request with fields and returns its response; the events
// that arrive before it are kept in p.events.
func (p *controlProcess) request(cmd string, fields map[string]any) map[string]any {
	p.t.Helper()
	p.nextID++
	request := map[string]any{"id": p.nextID, "cmd": cmd}
	for key, value := range fields {
		request[key] = value
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.stdin.Write(append(encoded, '\n')); err != nil {
		p.t.Fatalf("send %s: %v", encoded, err)
	}
	for {
		line := p.next()
		if _, isResponse := line["ok"]; !isResponse {
			p.events = append(p.events, line)
			continue
		}
		if line["id"] != float64(p.nextID) {
			p.t.Fatalf("response to %s has id %v: %v", encoded, line["id"], line)
		}
		return line
	}
}

// ok sends a request that must succeed and returns its result.
func (p *controlProcess) ok(cmd string, fields map[string]any) map[string]any {
	p.t.Helper()
	response := p.request(cmd, fields)
	if response["ok"] != true {
		p.t.Fatalf("%s %v failed: %v", cmd, fields, response)
	}
	result, _ := response["result"].(map[string]any)
	return result
}

// fails sends a request that must be rejected and returns its error.
func (p *controlProcess) fails(cmd string, fields map[string]any) string {
	p.t.Helper()
	response := p.request(cmd, fields)
	if response["ok"] != false || response["error"] == "" {
		p.t.Fatalf("%s %v was not rejected: %v", cmd, fields, response)
	}
	return fmt.Sprint(response["error"])
}

// data returns what the process wrote to stdout so far.
func (p *controlProcess) data() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stdout.String()
}

// waitForData waits until stdout holds text.
func (p *controlProcess) waitForData(text string) {
	p.t.Helper()
	deadline := time.Now().Add(controlWait)
	for !strings.Contains(p.data(), text) {
		if time.Now().After(deadline) {
			p.t.Fatalf("stdout lacks %q after %s:\n%s", text, controlWait, p.data())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// quit ends the session with a quit request and returns the exit error.
func (p *controlProcess) quit() error {
	p.t.Helper()
	if response := p.request("quit", nil); response["ok"] != true {
		p.t.Fatalf("quit: %v", response)
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(controlWait):
		p.t.Fatalf("the client did not exit within %s of quit", controlWait)
	}
	return nil
}

// controlArgv returns the command line of client with a JSON control
// channel reading file, against a new dserver or serverless. The client's
// log goes to a directory of the test, as its default with a control
// channel keeps diagnostics out of stdout.
func controlArgv(ctx context.Context, t *testing.T, client, file string, serverless bool,
	extra ...string) []string {
	t.Helper()
	argv := []string{"../" + client, "--cfg", "none", "--logDir", t.TempDir(), "--noColor",
		"--control", "json", "--files", file}
	argv = append(argv, extra...)
	if serverless {
		return argv
	}
	port, _ := startInteractiveTestServer(ctx, t)
	return append(argv, "--servers", fmt.Sprintf("localhost:%d", port), "--trustAllHosts")
}

// TestDMapControlJSON drives dmap without a query over its JSON control
// channel, as an agent does, against dserver and serverless: the session
// stays open while the fields of the file are read, a query is completed,
// the server's paths are listed, and several queries run, each answered
// with its final result. Nothing but the control channel is needed: no
// terminal, and stdout stays free of diagnostics.
func TestDMapControlJSON(t *testing.T) {
	skipIfNotIntegrationTest(t)
	for _, serverless := range []bool{false, true} {
		t.Run(fmt.Sprintf("serverless=%v", serverless), func(t *testing.T) {
			testDMapControlJSON(t, serverless)
		})
	}
}

func testDMapControlJSON(t *testing.T, serverless bool) {
	ctx, cancel := createTestContextWithTimeout(t)
	defer cancel()
	writeBuilderLog(t)
	p := startControlProcess(ctx, t, controlArgv(ctx, t, "dmap", builderLogFile, serverless))

	checkControlFields(t, p)
	checkControlCompletion(t, p)
	if reason := p.fails("query", map[string]any{"query": "select count("}); !strings.Contains(reason, "invalid query") {
		t.Fatalf("invalid query rejected with %q", reason)
	}
	if reason := p.fails("tail", nil); !strings.Contains(reason, "no \"tail\" command") {
		t.Fatalf("tail in dmap rejected with %q", reason)
	}

	// Two queries on one session, each with its complete, final result.
	total := p.ok("query", map[string]any{"query": "from STATS select count($line) group by $hostname"})
	if rows := fmt.Sprint(total["rows"]); total["final"] != true || !strings.HasPrefix(rows, fmt.Sprintf("[[%d", builderStatsLines)) ||
		total["status"] != float64(0) {
		t.Fatalf("count over all lines: %v", total)
	}
	// The servers read the whole file for it.
	info, err := os.Stat(builderLogFile)
	if err != nil {
		t.Fatal(err)
	}
	if read, _ := total["read"].(map[string]any); read["bytes"] != float64(info.Size()) || read["files"] != float64(1) {
		t.Fatalf("read totals of the query, file size %d: %v", info.Size(), total["read"])
	}
	grouped := p.ok("query", map[string]any{
		"query": "from STATS select currentConnections,count($line) group by currentConnections order by currentConnections"})
	if rows, _ := grouped["rows"].([]any); len(rows) != 3 || grouped["rowsTotal"] != float64(3) {
		t.Fatalf("count per currentConnections: %v", grouped)
	}

	// A file that does not exist: the run ends, its result is empty and
	// the warning names the reason.
	p.ok("reload", map[string]any{"args": []string{"--files", builderLogFile + ".missing"}})
	missing := p.ok("query", map[string]any{"query": "from STATS select count($line)"})
	if warnings := fmt.Sprint(missing["warnings"]); !strings.Contains(warnings, "no file to read") {
		t.Fatalf("query over a missing file: %v", missing)
	}

	if err := p.quit(); err != nil {
		t.Fatalf("dmap exited with %v", err)
	}
	if out := p.data(); strings.Contains(out, "|INFO|") || strings.Contains(out, "count($line)") {
		t.Fatalf("diagnostics or a result table on stdout:\n%s", out)
	}
}

// checkControlFields checks the catalog of the builder log: its one table
// with its fields, sample values and $variables, also probed on its own.
func checkControlFields(t *testing.T, p *controlProcess) {
	t.Helper()
	for _, fields := range []map[string]any{nil, {"table": "stats"}} {
		result := p.ok("fields", fields)
		encoded, _ := json.Marshal(result)
		for _, want := range []string{`"defaultLogFormat":"default"`, `"STATS":{"fields":{"currentConnections":[`,
			`"lifetimeConnections":[`, `"$hostname":[`} {
			if !strings.Contains(string(encoded), want) {
				t.Fatalf("fields %v lack %s: %s", fields, want, encoded)
			}
		}
	}
	listing := p.ok("list", map[string]any{"prefix": "dmap_builder"})
	if encoded, _ := json.Marshal(listing); !strings.Contains(string(encoded), `"name":"`+builderLogFile+`"`) {
		t.Fatalf("listing lacks %s: %s", builderLogFile, encoded)
	}
}

// checkControlCompletion checks what Tab would do to a query: the first
// step is from and the log's one table, and after select the fields of
// that table are offered.
func checkControlCompletion(t *testing.T, p *controlProcess) {
	t.Helper()
	start := p.ok("complete", map[string]any{"query": ""})
	if start["line"] != builderTabInserted || start["changed"] != true {
		t.Fatalf("completion of the empty query: %v", start)
	}
	items := p.ok("complete", map[string]any{"query": "from STATS select "})
	if candidates := fmt.Sprint(items["candidates"]); !strings.Contains(candidates, "currentConnections") ||
		!strings.Contains(candidates, "count(") {
		t.Fatalf("select items offered: %v", items)
	}
}

// TestDTailControlJSON drives a following dtail over its JSON control
// channel, against dserver and serverless: followed lines go to stdout, a
// query request switches the session to a MapReduce query whose results
// arrive as result events, tail switches back, and pause and resume stop
// and release the output, all on one session.
func TestDTailControlJSON(t *testing.T) {
	skipIfNotIntegrationTest(t)
	for _, serverless := range []bool{false, true} {
		t.Run(fmt.Sprintf("serverless=%v", serverless), func(t *testing.T) {
			testDTailControlJSON(t, serverless)
		})
	}
}

func testDTailControlJSON(t *testing.T, serverless bool) {
	ctx, cancel := createTestContextWithTimeout(t)
	defer cancel()
	file := filepath.Join(t.TempDir(), "follow.log")
	appendLines := controlLogAppender(t, file)
	appendLines("start", 1)
	p := startControlProcess(ctx, t, controlArgv(ctx, t, "dtail", file, serverless, "--plain", "--regex", "STATS"))

	// The session follows lines once the first appended one arrived.
	followLines(t, p, appendLines, "first")

	applied := p.ok("query", map[string]any{"query": "from STATS select count($line),$hostname group by $hostname interval 1"})
	if applied["workload"] != "query" {
		t.Fatalf("query applied: %v", applied)
	}
	result := awaitResultEvent(t, p, appendLines)
	if result["final"] != false || fmt.Sprint(result["columns"]) != "[count($line) $hostname]" {
		t.Fatalf("result event: %v", result)
	}

	if applied = p.ok("tail", nil); applied["workload"] != "tail" || applied["regex"] != "STATS" {
		t.Fatalf("tail applied: %v", applied)
	}
	followLines(t, p, appendLines, "second")

	// Paused, nothing is printed; the lines come once the output resumes.
	if paused := p.ok("pause", nil); paused["paused"] != true {
		t.Fatalf("pause: %v", paused)
	}
	if state := p.ok("show", nil); state["paused"] != true || state["workload"] != "tail" {
		t.Fatalf("state while paused: %v", state)
	}
	appendLines("held", 3)
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(p.data(), "marker=held") {
		t.Fatalf("lines printed while paused:\n%s", p.data())
	}
	p.ok("resume", nil)
	p.waitForData("marker=held")

	if err := p.quit(); err != nil {
		t.Fatalf("dtail exited with %v", err)
	}
}

// controlLogAppender creates file and returns a function that appends
// count STATS lines carrying marker to it.
func controlLogAppender(t *testing.T, file string) func(marker string, count int) {
	t.Helper()
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return func(marker string, count int) {
		t.Helper()
		fd, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fd.Close() }()
		for i := range count {
			if _, err := fmt.Fprintf(fd, guidedStatsLine+"|marker=%s\n", i, i, marker); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// followLines appends lines carrying marker until one of them is on stdout:
// a follow read starts at the end of the file as of its start, which the
// test can not see, so the first lines may be from before it.
func followLines(t *testing.T, p *controlProcess, appendLines func(string, int), marker string) {
	t.Helper()
	deadline := time.Now().Add(controlWait)
	for !strings.Contains(p.data(), "marker="+marker) {
		if time.Now().After(deadline) {
			t.Fatalf("no followed line with marker %s on stdout:\n%s", marker, p.data())
		}
		appendLines(marker, 1)
		time.Sleep(200 * time.Millisecond)
	}
}

// awaitResultEvent appends lines until a result event with rows arrived (a
// query on a follow read counts the lines arriving after it started), and
// returns it.
func awaitResultEvent(t *testing.T, p *controlProcess, appendLines func(string, int)) map[string]any {
	t.Helper()
	deadline := time.Now().Add(controlWait)
	for time.Now().Before(deadline) {
		appendLines("counted", 2)
		// show is answered at once; the events before its response are kept.
		p.ok("show", nil)
		for _, event := range p.events {
			if rows, _ := event["rows"].([]any); event["event"] == "result" && len(rows) > 0 {
				return event
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("no result event with rows within %s; events: %v", controlWait, p.events)
	return nil
}

// TestDMapQueryControlJSON runs dmap with a query and a JSON control
// channel, i.e. interactive dmap, serverless: the session runs the query
// and reports its results as result events, and the final result still
// reaches the control output after the quit request ended the control
// loop (the client writes it once its connections ended).
func TestDMapQueryControlJSON(t *testing.T) {
	skipIfNotIntegrationTest(t)
	ctx, cancel := createTestContextWithTimeout(t)
	defer cancel()
	writeBuilderLog(t)
	p := startControlProcess(ctx, t, controlArgv(ctx, t, "dmap", builderLogFile, true,
		"--query", "from STATS select count($line) group by $hostname interval 1"))

	interim := p.event("result")
	if rows := fmt.Sprint(interim["rows"]); interim["final"] != false || !strings.HasPrefix(rows, fmt.Sprintf("[[%d", builderStatsLines)) {
		t.Fatalf("interim result event: %v", interim)
	}
	if state := p.ok("show", nil); state["workload"] != "query" {
		t.Fatalf("state: %v", state)
	}
	if err := p.quit(); err != nil {
		t.Fatalf("dmap exited with %v", err)
	}
	for line := range p.lines {
		if line["event"] == "result" && line["final"] == true {
			return
		}
	}
	t.Fatal("no final result event after quit")
}
