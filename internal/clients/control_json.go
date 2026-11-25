package clients

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

// The JSON control channel (--control json, config.ControlJSON) is the
// interactive session for programs and agents: what a person does on the
// control TTY with the line editor (:fields, :query, :reload, ...), a
// program does with one JSON request per line, each answered by exactly one
// JSON line. The session, and with it the SSH connections, stays open
// between requests, so that a program can look at the fields of the logs,
// run a query, read the result and refine the query without connecting
// again.
//
//	request:   {"id": 1, "cmd": "fields", "table": "STATS"}
//	response:  {"id": 1, "ok": true, "result": {...}}
//	           {"id": 1, "ok": false, "error": "..."}
//	event:     {"event": "ready", ...}, {"event": "result", ...},
//	           {"event": "progress", "id": 1, ...}
//
// A response carries the id of its request as it was sent (any JSON value);
// events are the lines without "ok". There is no output pause, no prompt and
// no line editing: a host key question is answered no (use --trustAllHosts
// or known_hosts). Requests are handled one after the other, in order.
//
// The commands (controlCommands) call what the control TTY's commands call:
// the background prober and its catalog (fields, probe, list), the reload
// and workload switch of the session (reload, query, tail), the state
// (show), the Tab completion (complete), the output pause (pause, resume)
// and the help (help). In dmap started without a query the session holds no workload,
// as its query builder does, and a query request runs the query to its end
// on connections of its own and answers with the final result
// (clientProfile.runQuery, QueryBuilderClient.runQuery): only the plain
// command stream of such a run ends with the server's close handshake, which
// tells that the result is complete.

const (
	// controlProtocol is the version of the JSON control channel, sent with
	// the ready event; it changes when a request or a response changes its
	// meaning.
	controlProtocol = 1
	// controlMaxLine bounds one request line.
	controlMaxLine = 1 << 20
	// controlProbeWait is how long fields and list wait for the servers'
	// answer to the probe they sent before answering with what is known.
	controlProbeWait = 5 * time.Second
	// controlResultMaxRows caps the rows of a result without a limit clause,
	// so that a query over many groups does not flood its reader; the
	// result says how many rows there are.
	controlResultMaxRows = 200
)

// controlRequest is one request line. Which fields a command reads is in
// its handler (control_json_commands.go).
type controlRequest struct {
	// ID is echoed in the response, as sent.
	ID json.RawMessage `json:"id,omitempty"`
	// Cmd is the command name (controlCommands).
	Cmd string `json:"cmd"`
	// Table is the table of fields.
	Table string `json:"table,omitempty"`
	// Prefix is the path prefix of list.
	Prefix string `json:"prefix,omitempty"`
	// Query is the MapReduce query of query.
	Query string `json:"query,omitempty"`
	// Regex is the regex of tail; without it tail follows the regex
	// followed before.
	Regex string `json:"regex,omitempty"`
	// Line is the control line complete completes, as it would be typed on
	// the control TTY (":reload --files /var/l", ":query from ST"), and
	// Cursor the byte offset in it to complete at (nil: its end). With
	// Query set instead, complete completes that query.
	Line   string `json:"line,omitempty"`
	Cursor *int   `json:"cursor,omitempty"`
	// Args are the flags of reload, one argument each, as on a command
	// line: ["--files", "/var/log/a.log", "--regex", "ERROR"]. No quoting
	// rules apply.
	Args []string `json:"args,omitempty"`
}

// controlResponse answers one request.
type controlResponse struct {
	ID     json.RawMessage `json:"id,omitempty"`
	OK     bool            `json:"ok"`
	Result any             `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// controlReady is the first line of the channel: the session takes requests.
type controlReady struct {
	Event    string   `json:"event"`
	Protocol int      `json:"protocol"`
	Mode     string   `json:"mode"`
	Workload string   `json:"workload"`
	Files    []string `json:"files"`
	Servers  int      `json:"servers"`
	Commands []string `json:"commands"`
}

// controlWriter writes the lines of the control channel, one JSON value
// each. It exists from the client's construction on, so that the goroutines
// that report results can hold it, and writes nowhere until open gave it
// its target (startJSONControl); lines written before are dropped.
type controlWriter struct {
	mu  sync.Mutex
	out io.Writer
	err error
}

// open sets where the lines go.
func (w *controlWriter) open(out io.Writer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.out = out
}

// write writes value as one line. The first write error is kept (failed)
// and ends the control loop: a channel nobody reads any more has no use.
func (w *controlWriter) write(value any) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.out == nil || w.err != nil {
		return
	}
	encoder := json.NewEncoder(w.out)
	// The reader is a program: <, > and & in a query or a log value stay
	// what they are.
	encoder.SetEscapeHTML(false)
	w.err = encoder.Encode(value)
}

// failed returns the error that ended the writer, if any.
func (w *controlWriter) failed() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// isJSONControl reports whether args ask for the JSON control channel.
func isJSONControl(args config.Args) bool {
	return args.Control == config.ControlJSON
}

// openControlChannel opens the request input and the response output of the
// JSON control channel: stdin and stderr, or the paths given. A FIFO as
// input is opened for reading and writing, so that it does not end when a
// program that wrote one request closes it; the output path is appended to.
// The returned closeIn closes an input opened here, which also ends a
// pending read of it. The output stays open for the life of the process:
// the client writes the last result of a query after the control loop
// ended (MaprClient.finish), and every line is written at once, unbuffered.
func openControlChannel(inPath, outPath string) (in io.Reader, out io.Writer, closeIn func(), err error) {
	closeIn = func() {}
	in, out = os.Stdin, os.Stderr
	if outPath != "" {
		file, openErr := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if openErr != nil {
			return nil, nil, closeIn, fmt.Errorf("open control output: %w", openErr)
		}
		out = file
	}
	if inPath != "" {
		file, openErr := openControlInput(inPath)
		if openErr != nil {
			return nil, nil, closeIn, fmt.Errorf("open control input: %w", openErr)
		}
		in = file
		closeIn = func() { _ = file.Close() }
	}
	return in, out, closeIn, nil
}

// openControlInput opens path for the requests: a FIFO read-write (see
// openControlChannel), anything else read-only.
func openControlInput(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeNamedPipe != 0 {
		return os.OpenFile(path, os.O_RDWR, 0)
	}
	return os.Open(path)
}

// startJSONControl is startInteractiveControl for the JSON control channel:
// it runs the connections, the background prober and the request loop until
// the connections end, a quit request, the end of the input or the end of
// ctx, and returns the session's exit status.
func (c *baseClient) startJSONControl(ctx context.Context, statsCh <-chan string) int {
	in, out, closeIn, err := openControlChannel(c.ControlIn, c.ControlOut)
	if err != nil {
		c.clientLogger().Error("Unable to open the control channel", err)
		return 1
	}
	c.control.open(out)

	runCtx, cancel := context.WithCancel(ctx)
	c.prober = c.newInteractiveProber()
	defer func() {
		cancel()
		c.prober.wait()
		closeIn()
	}()
	// Nobody answers a host key question here, and it must not read the
	// requests from stdin: it gets the end of its input, which is a no.
	if router, ok := c.hostKeyCallback.(promptRouter); ok {
		router.SetPromptInput(strings.NewReader(""))
	}

	statusCh := make(chan int, 1)
	go func() {
		statusCh <- c.runConnections(runCtx, statsCh)
	}()
	c.prober.start(runCtx)
	if isQueryBuilder(c.Args) {
		go c.keepSessionsAlive(runCtx, builderKeepAliveInterval, c.heldConnections)
	}
	go func() {
		// A read of the input still pending at the end ends with its file.
		<-runCtx.Done()
		closeIn()
	}()

	controlErrCh := make(chan error, 1)
	go func() {
		controlErrCh <- c.runJSONControl(runCtx, cancel, in)
	}()
	return c.awaitInteractiveEnd(cancel, statusCh, controlErrCh)
}

// runJSONControl announces the session (ready) and handles the requests of
// in, one line each and one after the other, until a quit request, the end
// of in or of ctx, or a failed write of a response.
func (c *baseClient) runJSONControl(ctx context.Context, cancel context.CancelFunc, in io.Reader) error {
	// The output pause of the pause and resume requests; like the control
	// TTY's, it is resumed whenever the loop ends, so that writers it
	// blocks can finish and the client shuts down.
	session := &controlSession{client: c, pause: c.newInteractivePause()}
	defer session.pause.end()
	c.control.write(c.controlReadyEvent())
	lines := readControlLines(ctx, in)
	for {
		var line controlLine
		var ok bool
		select {
		case <-ctx.Done():
			return nil
		case line, ok = <-lines:
		}
		if !ok {
			return nil
		}
		if line.err != nil {
			return line.err
		}
		if session.handleLine(ctx, line.text) {
			cancel()
			return nil
		}
		if err := c.control.failed(); err != nil {
			return fmt.Errorf("write to the control channel: %w", err)
		}
	}
}

// controlLine is one line read from the control input, or the error that
// ended the reading.
type controlLine struct {
	text string
	err  error
}

// readControlLines reads the lines of in on a goroutine of its own, as a
// read can not be cancelled, and closes the returned channel at the end of
// in. A line over controlMaxLine ends the reading with an error; the end of
// ctx ends the hand-over of lines.
func readControlLines(ctx context.Context, in io.Reader) <-chan controlLine {
	lines := make(chan controlLine)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), controlMaxLine)
		for scanner.Scan() {
			select {
			case lines <- controlLine{text: scanner.Text()}:
			case <-ctx.Done():
				return
			}
		}
		err := scanner.Err()
		if err == nil || ctx.Err() != nil || errors.Is(err, os.ErrClosed) {
			return
		}
		select {
		case lines <- controlLine{err: fmt.Errorf("read the control channel: %w", err)}:
		case <-ctx.Done():
		}
	}()
	return lines
}

// controlSession is the state of one JSON control loop: its client and the
// output pause its pause and resume requests drive.
type controlSession struct {
	client *baseClient
	pause  *interactivePause
}

// handleLine handles one request line and writes its response; a blank
// line is skipped. It reports whether the session is to end (quit).
func (s *controlSession) handleLine(ctx context.Context, line string) (quit bool) {
	c := s.client
	if strings.TrimSpace(line) == "" {
		return false
	}
	request, err := parseControlRequest(line)
	if err != nil {
		c.control.write(controlResponse{ID: request.ID,
			Error: "invalid request, expected one JSON object per line: " + err.Error()})
		return false
	}
	if request.Cmd == "quit" {
		c.control.write(controlResponse{ID: request.ID, OK: true})
		return true
	}
	result, err := s.run(ctx, request)
	if err != nil {
		c.control.write(controlResponse{ID: request.ID, Error: errorLines(err)})
		return false
	}
	c.control.write(controlResponse{ID: request.ID, OK: true, Result: result})
	return false
}

// parseControlRequest decodes one request line. A field no command reads
// is an error, not ignored: a misspelt "table" or "query" would otherwise
// run the command without it and answer something else than was asked. The
// id is returned also for a line that fails so, if it could be read, so
// that its sender finds the response.
func parseControlRequest(line string) (controlRequest, error) {
	var request controlRequest
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var lenient controlRequest
		_ = json.Unmarshal([]byte(line), &lenient)
		return controlRequest{ID: lenient.ID}, err
	}
	if decoder.More() {
		return controlRequest{ID: request.ID}, errors.New("more than one JSON value on the line")
	}
	return request, nil
}

// controlReadyEvent is the ready event: what the session is and which
// commands it takes.
func (c *baseClient) controlReadyEvent() controlReady {
	args, _, connections := c.snapshotMutableState()
	return controlReady{
		Event:    "ready",
		Protocol: controlProtocol,
		Mode:     args.Mode.String(),
		Workload: workloadName(args),
		Files:    nonNilStrings(c.sessionFiles()),
		Servers:  len(connections),
		Commands: controlCommandNames(args),
	}
}

// nonNilStrings returns values, or an empty slice for nil, so that JSON has
// [] instead of null.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
