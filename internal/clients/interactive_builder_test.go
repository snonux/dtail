package clients

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

// builderTestArgs are the arguments of a query builder reading files.
func builderTestArgs(files string) config.Args {
	return config.Args{Mode: omode.MapClient, QueryBuilder: true, InteractiveQuery: true, What: files}
}

// newBuilderTestClient returns a base client in the query builder's state
// that records the query handed over, with a prober (not started) so that
// the no-files check sees the client's files.
func newBuilderTestClient(files string) (*baseClient, *builtQuery) {
	built := &builtQuery{}
	c := &baseClient{mu: newBaseClientMu(), Args: builderTestArgs(files),
		loggers: LoggerDependencies{}.normalized()}
	c.sessionSpec = startSessionSpec(c.Args)
	c.profile.queryBuilt = func(files, query string) { *built = builtQuery{files: files, query: query} }
	c.prober = c.newInteractiveProber()
	return c, built
}

// TestBuilderHoldSpecHoldsNoWorkload checks the spec the builder's sessions
// start with: tail mode without files, so it yields no command however
// many files the builder was given, while every other client starts with
// the workload of its arguments.
func TestBuilderHoldSpecHoldsNoWorkload(t *testing.T) {
	spec := startSessionSpec(builderTestArgs("/var/log/a.log,/var/log/b.log"))
	if spec.Mode != omode.TailClient || len(spec.Files) != 0 || spec.Query != "" {
		t.Fatalf("hold spec = %+v, want tail mode without files and query", spec)
	}
	commands, err := spec.Commands()
	if err != nil || len(commands) != 0 {
		t.Fatalf("hold spec commands = %q, %v, want none", commands, err)
	}
	if _, err := spec.StartCommand(); err != nil {
		t.Fatalf("hold spec has no SESSION START: %v", err)
	}

	query := config.Args{Mode: omode.MapClient, What: "/var/log/a.log", QueryStr: "from STATS select count($line)"}
	if got := startSessionSpec(query); got.Mode != omode.MapClient || got.Query == "" || len(got.Files) != 1 {
		t.Fatalf("start spec of dmap --query = %+v, want its workload", got)
	}
}

// TestBuilderSessionFiles checks that the prober of the builder probes the
// files the query will read although its sessions hold none, also after a
// :reload --files, and that other sessions keep probing their spec's files.
func TestBuilderSessionFiles(t *testing.T) {
	c, _ := newBuilderTestClient("/var/log/a.log, /var/log/b.log")
	if got := strings.Join(c.sessionFiles(), "|"); got != "/var/log/a.log|/var/log/b.log" {
		t.Fatalf("builder files = %q", got)
	}
	var out bytes.Buffer
	if quit, err := c.handleBuilderLine(&out, ":reload --files /var/log/c.log", c.newInteractivePause()); quit || err != nil {
		t.Fatalf("reload: quit %v, err %v", quit, err)
	}
	if got := strings.Join(c.sessionFiles(), "|"); got != "/var/log/c.log" {
		t.Fatalf("builder files after :reload = %q (answer %q)", got, out.String())
	}
	if _, spec := c.snapshotConnectionState(); len(spec.Files) != 0 {
		t.Fatalf("the held session spec got files: %+v", spec)
	}

	tail := &baseClient{mu: newBaseClientMu(), Args: config.Args{Mode: omode.TailClient, What: "ignored"},
		sessionSpec: SessionSpec{Mode: omode.TailClient, Files: []string{"/var/log/t.log"}}}
	if got := strings.Join(tail.sessionFiles(), "|"); got != "/var/log/t.log" {
		t.Fatalf("tail session files = %q, want those of its spec", got)
	}
}

// TestParseBuilderCommand covers the lines the builder takes: a line
// without a leading colon and the argument of :query are the query, :reload
// takes the files, and the other commands are the control loop's.
func TestParseBuilderCommand(t *testing.T) {
	const query = "from STATS select count($line) group by $hostname"
	current := builderTestArgs("/var/log/app.log")
	tests := []struct {
		line      string
		wantKind  string
		wantValue string
	}{
		{line: query, wantKind: "build", wantValue: query},
		{line: "  " + query + " \r", wantKind: "build", wantValue: query},
		{line: ":query " + query, wantKind: "build", wantValue: query},
		{line: ":query", wantKind: "showquery"},
		{line: ":reload --files /var/log/other.log", wantKind: "files"},
		{line: ":help", wantKind: "help"},
		{line: ":show", wantKind: "show"},
		{line: ":fields STATS", wantKind: "fields"},
		{line: ":probe", wantKind: "probe"},
		{line: ":quit", wantKind: "quit"},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			command, err := parseBuilderCommand(current, tt.line)
			if err != nil || command.kind != tt.wantKind || command.value != tt.wantValue {
				t.Fatalf("command = kind %q value %q, err %v; want kind %q value %q",
					command.kind, command.value, err, tt.wantKind, tt.wantValue)
			}
		})
	}
	command, err := parseBuilderCommand(current, ":reload --files /var/log/other.log")
	if err != nil || command.next.What != "/var/log/other.log" || !command.next.QueryBuilder {
		t.Fatalf(":reload --files: next %+v, err %v", command.next, err)
	}
}

// TestParseBuilderCommandRejects covers the lines the builder rejects: a
// query that does not parse or is no valid UTF-8, a :reload with anything
// but --files, :tail, an unknown command, and a command word typed without
// its colon, which is answered with the command meant rather than with the
// parser's error for a query without a select clause.
func TestParseBuilderCommandRejects(t *testing.T) {
	const query = "from STATS select count($line) group by $hostname"
	current := builderTestArgs("/var/log/app.log")
	tests := []struct {
		line    string
		wantErr string
	}{
		{line: "from STATS select count(", wantErr: "invalid query"},
		{line: "selec foo", wantErr: "invalid query"},
		{line: ":query from STATS select count(", wantErr: "invalid query"},
		{line: "from STATS select \xff", wantErr: "not valid UTF-8"},
		{line: ":reload", wantErr: "reload requires flags"},
		{line: ":reload --files ''", wantErr: "--files needs at least one non-blank file"},
		{line: ":reload --plain", wantErr: "flag provided but not defined"},
		{line: ":reload --query '" + query + "'", wantErr: "flag provided but not defined"},
		{line: ":tail ERROR", wantErr: ":tail is only available in dtail"},
		{line: ":bogus", wantErr: "unknown command"},
		{line: "quit", wantErr: `"quit" is no query: type :quit`},
		{line: " Exit ", wantErr: `"Exit" is no query: type :quit`},
		{line: "help", wantErr: `"help" is no query: type :help`},
		{line: "fields", wantErr: `"fields" is no query: type :fields`},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			command, err := parseBuilderCommand(current, tt.line)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q (command %+v)", err, tt.wantErr, command)
			}
		})
	}
	// A query that merely starts with such a word is a query.
	if _, err := parseBuilderCommand(current, "help me"); err == nil || strings.Contains(err.Error(), ":help") {
		t.Fatalf("\"help me\" was taken for the help command: %v", err)
	}
}

// builderLineTest is one case of TestHandleBuilderLine: the lines entered
// in a builder reading files, whether the last one ends it, the files
// handed over with builderLineQuery (none: nothing is handed over) and a
// part of the answers.
type builderLineTest struct {
	name       string
	files      string
	lines      []string
	wantQuit   bool
	wantFiles  string
	wantAnswer string
}

// builderLineQuery is the query of the builderLineTests.
const builderLineQuery = "from STATS select count($line)"

func builderLineTests() []builderLineTest {
	const files, query = "/var/log/a.log", builderLineQuery
	return []builderLineTest{
		{name: "query runs", files: files, lines: []string{query},
			wantQuit: true, wantFiles: files, wantAnswer: "running the query\nquery: " + query},
		{name: ":query runs", files: files, lines: []string{":query " + query},
			wantQuit: true, wantFiles: files, wantAnswer: "running the query"},
		{name: "files of a reload", files: files,
			lines:    []string{":reload --files /var/log/b.log,/var/log/c.log", query},
			wantQuit: true, wantFiles: "/var/log/b.log,/var/log/c.log", wantAnswer: "files set"},
		{name: "parse error", files: files, lines: []string{"from STATS select count("},
			wantAnswer: builderErrorPrefix + "invalid query"},
		{name: "unknown command", files: files, lines: []string{":bogus"},
			wantAnswer: builderErrorPrefix + `unknown command ":bogus"`},
		{name: "command word without its colon", files: files, lines: []string{"quit"},
			wantAnswer: builderErrorPrefix + `"quit" is no query: type :quit`},
		{name: "empty line", files: files, lines: []string{"  "}, wantAnswer: builderEmptyLineHint},
		{name: ":query alone", files: files, lines: []string{":query"}, wantAnswer: builderNoQueryText},
		{name: ":show", files: files, lines: []string{":show"},
			wantAnswer: `mode=map workload=builder files=/var/log/a.log query="" servers=0 sessions=0`},
		{name: "no files", lines: []string{query},
			wantAnswer: builderErrorPrefix + "query not run, " + sessionNoFilesText},
		{name: ":quit", files: files, lines: []string{":quit"}, wantQuit: true,
			wantAnswer: "quitting interactive session"},
	}
}

// TestHandleBuilderLine runs lines through the builder's line handler: only
// a query that parses is handed over and ends the builder, with the files
// of that moment; rejected lines, an empty line and the other commands
// keep it open and hand nothing over.
func TestHandleBuilderLine(t *testing.T) {
	for _, tt := range builderLineTests() {
		t.Run(tt.name, func(t *testing.T) {
			c, built := newBuilderTestClient(tt.files)
			var out bytes.Buffer
			var quit bool
			for _, line := range tt.lines {
				var err error
				if quit, err = c.handleBuilderLine(&out, line, c.newInteractivePause()); err != nil {
					t.Fatalf("line %q: %v", line, err)
				}
			}
			if quit != tt.wantQuit || !strings.Contains(out.String(), tt.wantAnswer) {
				t.Fatalf("quit = %v, want %v; answer %q, want it to contain %q",
					quit, tt.wantQuit, out.String(), tt.wantAnswer)
			}
			if want := (builtQuery{}); tt.wantFiles == "" && *built != want {
				t.Fatalf("handed over %+v, want nothing", *built)
			}
			if tt.wantFiles != "" && (built.files != tt.wantFiles || built.query != builderLineQuery) {
				t.Fatalf("handed over %+v, want files %q and query %q", *built, tt.wantFiles, builderLineQuery)
			}
		})
	}
}

// TestBuilderFilesNeedJournalSupport: :reload --files with a journal target
// is refused while a server whose session is up lacks journal-v1, as a
// :reload of a running workload is, and the files stay as they were;
// with every such server supporting it, and for plain files, it is taken.
func TestBuilderFilesNeedJournalSupport(t *testing.T) {
	const journal = ":reload --files journal:app.service"
	for _, tt := range []struct {
		name      string
		supported bool
		line      string
		wantFiles string
		wantError string
	}{
		{name: "a server without journal-v1", line: journal, wantFiles: "/var/log/a.log",
			wantError: builderErrorPrefix + "files not set: "},
		{name: "every server with journal-v1", supported: true, line: journal, wantFiles: "journal:app.service"},
		{name: "plain files need none", line: ":reload --files /var/log/b.log", wantFiles: "/var/log/b.log"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newBuilderTestClient("/var/log/a.log")
			with := newInteractiveReloadSessionHandler(handlers.SessionAck{Action: "update", Generation: 2})
			with.capabilities[protocol.CapabilityJournalV1] = true
			other := newInteractiveReloadSessionHandler(handlers.SessionAck{Action: "update", Generation: 2})
			other.capabilities[protocol.CapabilityJournalV1] = tt.supported
			c.connections = []connectors.Connector{
				newInteractiveReloadServerConnection(t, "srv1", with, c.sessionSpec),
				newInteractiveReloadServerConnection(t, "srv2", other, c.sessionSpec),
			}
			var out bytes.Buffer
			if _, err := c.handleBuilderLine(&out, tt.line, c.newInteractivePause()); err != nil {
				t.Fatal(err)
			}
			if c.What != tt.wantFiles {
				t.Fatalf("files = %q, want %q (answer %q)", c.What, tt.wantFiles, out.String())
			}
			if tt.wantError != "" && (!strings.Contains(out.String(), tt.wantError) ||
				!strings.Contains(out.String(), "srv2") || strings.Contains(out.String(), "srv1")) {
				t.Fatalf("answer %q, want the refusal naming srv2 only", out.String())
			}
			if tt.wantError == "" && !strings.Contains(out.String(), "files set") {
				t.Fatalf("answer %q, want the files set", out.String())
			}
		})
	}
}

// TestBuilderLineResumesTypingPause checks that a line of the builder never
// leaves the output paused: its keys paused it for typing, and neither a
// rejected line nor :help (which keeps other sessions paused) keeps it so.
func TestBuilderLineResumesTypingPause(t *testing.T) {
	for _, line := range []string{"from STATS select count(", ":help", ":fields", "", ":show"} {
		c, _ := newBuilderTestClient("/var/log/a.log")
		logger := newPausingTestLogger()
		c.loggers.Client = logger
		pause := c.newInteractivePause()
		pause.pauseTyping()
		var out bytes.Buffer
		if _, err := c.handleBuilderLine(&out, line, pause); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		waitForPauseDepth(t, logger, 0)
		if pause.paused {
			t.Fatalf("line %q left the output paused (answer %q)", line, out.String())
		}
		if strings.Contains(out.String(), interactivePausedMessage) {
			t.Fatalf("line %q announced a pause: %q", line, out.String())
		}
	}
}

// TestBuilderCompletion checks Tab in the builder: a line that is no
// control command is completed as a query (as after ":query "), a line
// starting with a colon as a command, and :reload offers --files only.
func TestBuilderCompletion(t *testing.T) {
	args := builderTestArgs("/var/log/app.log")
	catalog := func() catalogSnapshot { return catalogSnapshot{} }
	tests := []struct {
		line string
		want string
	}{
		{line: "", want: "from "},
		{line: "from STATS sel", want: "from STATS select "},
		{line: "  from STATS sel", want: "  from STATS select "},
		{line: ":qui", want: ":quit"},
		{line: ":query from STATS sel", want: ":query from STATS select "},
		{line: ":reload ", want: ":reload --files "},
		{line: ":reload --files ", want: ":reload --files /var/log/app.log"},
	}
	for _, tt := range tests {
		got := completeInteractive(tt.line, len(tt.line), args, catalog)
		if got.line != tt.want {
			t.Errorf("Tab on %q = %q (candidates %q), want %q", tt.line, got.line, got.candidates, tt.want)
		}
	}
	// The cursor in front of a control command completes no query.
	if got := completeInteractive(":quit", 0, args, catalog); strings.Contains(got.line, "from") {
		t.Errorf("Tab in front of :quit started a query: %+v", got)
	}
	// Outside the builder a bare line is no query: nothing is inserted.
	plain := config.Args{Mode: omode.MapClient, What: "/var/log/app.log", QueryStr: "from STATS select count($line)"}
	if got := completeInteractive("from STATS sel", 14, plain, catalog); got.changed {
		t.Errorf("Tab completed a bare query outside the builder: %+v", got)
	}
}

// TestBuilderHelp checks the builder's :help: every example line is a line
// the builder takes (a query that parses or a command), and it is painted
// with a heading for the commands and for the examples, like every help.
func TestBuilderHelp(t *testing.T) {
	args := builderTestArgs("/var/log/app.log")
	text := interactiveHelpText(args)
	if !strings.HasPrefix(text, "Commands: ") {
		t.Fatalf("help starts with %q", text[:min(len(text), 20)])
	}
	examples := 0
	for _, line := range strings.Split(text, "\n") {
		example, found := strings.CutPrefix(line, "  ")
		if !found {
			continue
		}
		if i := strings.Index(example, "  "); i > 0 {
			example = example[:i]
		}
		examples++
		if _, err := parseBuilderCommand(args, example); err != nil {
			t.Errorf("help example %q is rejected: %v", example, err)
		}
	}
	if examples < 3 {
		t.Fatalf("help has %d examples", examples)
	}

	s := testInteractiveStyle()
	theme := config.DefaultTermColors().Interactive
	headings := 0
	for i, line := range strings.Split(s.help(text), "\n") {
		plain := strings.Split(text, "\n")[i]
		switch {
		case line == s.paint(theme.Heading, plain):
			headings++
		case strings.HasPrefix(plain, "  "):
			if !strings.HasPrefix(line, "  "+s.paint(theme.Command, strings.Fields(plain)[0])[:5]) {
				t.Errorf("example %q is not painted as a command: %q", plain, line)
			}
		case line != plain:
			t.Errorf("line %q is painted: %q", plain, line)
		}
	}
	if headings != 2 {
		t.Fatalf("%d headings painted, want 2 (commands, examples)", headings)
	}
}

// TestBuilderBanner checks that the builder says what to do, with Tab only
// where the line editor completes, and that other sessions keep theirs.
func TestBuilderBanner(t *testing.T) {
	plain := &scannerLines{}
	if got := controlBanner(plain, true); got != builderPlainBanner || strings.Contains(got, "Tab") {
		t.Errorf("plain builder banner = %q", got)
	}
	if got := controlBanner(&interactiveEditor{}, true); got != builderBanner || !strings.Contains(got, "Tab") {
		t.Errorf("builder banner = %q", got)
	}
	if got := controlBanner(&interactiveEditor{}, false); got != interactiveBanner {
		t.Errorf("banner outside the builder = %q", got)
	}
}
