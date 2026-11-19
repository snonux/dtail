package clients

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/omode"
)

// The query builder is what dmap opens when it is started without --query
// on a terminal (NewQueryBuilderClient): the control loop, line editor and
// background prober of an interactive session, on sessions that hold no
// workload (builderHoldSpec). In it the line typed is the query; lines
// starting with ":" are control commands. This file holds what the control
// loop does differently in the builder; the two phases of the run are
// described at QueryBuilderClient.
const (
	// builderBanner is the banner of the builder with the line editor,
	// builderPlainBanner the one without (no Tab completion).
	builderBanner = "No query given: build one and press Enter to run it. Tab guides through the query, " +
		":fields lists the tables and fields found, :help helps, :quit leaves without running."
	builderPlainBanner = "No query given: type one and press Enter to run it. " +
		":fields lists the tables and fields found, :help helps, :quit leaves without running."
	// builderPrompt is in front of every line of the builder (see
	// interactiveStyle.editorPrompt): nothing streams there that a prompt
	// could overwrite.
	builderPrompt = "query> "
	// builderEmptyLineHint answers an empty line: there is no output to
	// pause or resume in the builder.
	builderEmptyLineHint = "type a MapReduce query and press Enter to run it " +
		"(:help helps, :quit leaves without running)"
	// builderNoQueryText answers :query alone and is the query of :show.
	builderNoQueryText = "no query yet: type one and press Enter to run it"
	// builderErrorPrefix is in front of every rejected line of the builder.
	builderErrorPrefix = "query builder: "
	// builderQueryAsFilesHint follows the banner when the files look like
	// a query (queryAsFilesHint); %s is the files value, quoted.
	builderQueryAsFilesHint = "the files %s look like a query (dmap takes a query from the command line " +
		"with --query, or as an argument that holds \"select \"): type the query here and name the files " +
		"with :reload --files PATH"
)

// builderCommandWords are the words that, typed alone without their colon,
// are meant as the command and not as a query (bareCommandHint).
var builderCommandWords = map[string]string{
	"quit": ":quit", "exit": ":quit", "q": ":quit", "help": ":help", "show": ":show",
	"fields": ":fields", "probe": ":probe", "reload": ":reload --files PATH", "query": "the query itself",
}

// queryAsFilesHint returns, for the query builder, the hint that its files
// look like a query, "" when they do not: the first of several words is a
// clause keyword of the query language (from, select, where, ...). dmap
// takes a positional argument for the query only when it holds "select "
// (config.setupAdditionalArgs); a query with that word mistyped, or one
// that lacks it, is taken for a file, so no query is given, the builder
// opens, and its probes of a file of that name find nothing. A file whose
// name starts that way gets the hint too, which changes nothing else.
func queryAsFilesHint(args config.Args) string {
	if !isQueryBuilder(args) {
		return ""
	}
	words := strings.Fields(args.What)
	if len(words) < 2 || !slices.Contains(mapr.Keywords(), strings.ToLower(words[0])) {
		return ""
	}
	return fmt.Sprintf(builderQueryAsFilesHint, strconv.Quote(args.What))
}

// isQueryBuilder reports whether args are those of dmap's query builder
// (config.Args.QueryBuilder, set by NewQueryBuilderClient): the MapReduce
// client mode with the query still to be entered.
func isQueryBuilder(args config.Args) bool {
	return args.QueryBuilder
}

// startSessionSpec returns the session spec a client with args starts its
// connections with: the workload of args, or for the query builder the
// spec that holds its sessions without one (builderHoldSpec).
func startSessionSpec(args config.Args) SessionSpec {
	if isQueryBuilder(args) {
		return builderHoldSpec(args)
	}
	return NewSessionSpec(args)
}

// builderHoldSpec is the session spec of the query builder's connections:
// tail mode without files, which yields no command at all. It is what an
// interactive dtail without --files starts its session with, so every
// protocol 5 dserver (and the serverless runtime) takes it as it is and
// keeps the session, which runs nothing, until the client ends it; the
// probes of the builder need no more than a session. The map mode can not
// hold a session: without a query dserver rejects its spec ("missing
// session query"). The files of args are deliberately not in it, as a tail
// spec with files would follow them; the prober reads them from the args
// (sessionFiles).
func builderHoldSpec(args config.Args) SessionSpec {
	return SessionSpec{Mode: omode.TailClient, Options: args.SerializeOptions(), Regex: "."}
}

// sessionFiles returns the files the session reads, for the prober: those
// of the session spec, or for the query builder, whose spec holds none
// (builderHoldSpec), those the built query will read.
func (c *baseClient) sessionFiles() []string {
	args, spec := c.snapshotConnectionState()
	if isQueryBuilder(args) {
		return NewSessionSpec(args).Files
	}
	return spec.Files
}

// handleBuilderLine handles one line of the query builder other than a
// host key answer: a control command if it starts with ":", else the query
// (parseBuilderCommand). The line's keys paused the output (the client's
// own log lines) for typing; it is resumed once the line was answered, or
// before a command that needs the servers' answers (resumesBeforeRun). An
// empty line pauses nothing, as the builder has no output to hold back,
// and is answered with what to type. A rejected line (a query that does
// not parse, an unknown command) is answered and the builder stays open;
// quit is reported for :quit and for a query to run.
func (c *baseClient) handleBuilderLine(tty io.Writer, line string, pause *interactivePause) (bool, error) {
	defer pause.resume()
	style := styleOf(tty)
	if strings.TrimSpace(line) == "" {
		return false, writeControlLine(tty, style.hint(builderEmptyLineHint))
	}
	current, _ := c.snapshotConnectionState()
	command, err := parseBuilderCommand(current, line)
	if err != nil {
		return false, writeControlLine(tty, style.failure(builderErrorPrefix+errorLines(err)))
	}
	if resumesBeforeRun(command.kind) {
		pause.resume()
	}
	if command.kind == "fields" && command.table != "" {
		c.awaitTableProbe(pause, command.table)
	}
	return c.runInteractiveCommand(tty, command)
}

// parseBuilderCommand parses one line of the query builder: a line that
// does not start with ":" is the query, as is the argument of :query
// (parseBuiltQuery); :reload takes the files to read (parseBuilderFiles);
// every other command is the control loop's (parseInteractiveCommand:
// :fields, :probe, :show, :help, :quit; :tail is rejected there, as
// outside dtail).
func parseBuilderCommand(current config.Args, line string) (interactiveCommand, error) {
	trimmed := strings.TrimSpace(line)
	switch {
	case bareCommandHint(trimmed) != nil:
		return interactiveCommand{}, bareCommandHint(trimmed)
	case !strings.HasPrefix(trimmed, ":"):
		return parseBuiltQuery(current, trimmed)
	case isCommandLine(trimmed, queryCommand):
		query, err := commandArgument(line, queryCommand)
		if err != nil {
			return interactiveCommand{}, err
		}
		if query == "" {
			return interactiveCommand{kind: "showquery"}, nil
		}
		return parseBuiltQuery(current, query)
	case isInteractiveReloadLine(trimmed):
		return parseBuilderFiles(current, line)
	default:
		return parseInteractiveCommand(current, line)
	}
}

// bareCommandHint returns, for a line that is one of the builder's command
// words without its colon (quit, exit, help, ...), the error that names the
// command meant; nil for any other line. As a query such a word only gets
// the parser's error about a query that has no select clause, which does
// not tell how to leave.
func bareCommandHint(trimmed string) error {
	command, found := builderCommandWords[strings.ToLower(trimmed)]
	if !found {
		return nil
	}
	return fmt.Errorf("%q is no query: type %s", trimmed, command)
}

// discardQuietTime is how long the control TTY must have been quiet for
// drainTTYInput to be done, discardMaxTime how long it discards at most.
const (
	discardQuietTime = 100 * time.Millisecond
	discardMaxTime   = time.Second
)

// discardTTYInput discards what the control TTY holds unread (tcflush).
// Failures are ignored: a control TTY that is no terminal has no such
// queue.
func discardTTYInput(tty *os.File) {
	raw, err := tty.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) { _ = flushTTYInput(int(fd)) })
}

// drainTTYInput discards the input of the control TTY when the builder
// ends, until the terminal was quiet for discardQuietTime (at most
// discardMaxTime): keys typed or text pasted after the line that ran the
// query or left the builder. Read later they would be taken by the run (an
// interactive one takes them as control lines) or, after dmap exited, by
// the shell, which would run pasted lines as commands. One tcflush is not
// enough: it discards what the kernel has queued (4096 bytes on a Linux
// pseudo terminal), while the rest of a paste in flight is written by the
// terminal only as the queue empties, i.e. after the flush. So the queue
// is flushed, and read, until nothing arrived for the quiet time. The
// editor itself does not enter a line that input follows
// (editInput.settle), so this is for input that came later than that. It
// must run in the goroutine that reads the control lines, after its last
// read: a read of another goroutine would take the input instead. Where
// reads of the TTY take no deadline (no runtime poller for terminals) it
// flushes once.
func drainTTYInput(tty *os.File) {
	defer func() { _ = tty.SetReadDeadline(time.Time{}) }()
	end := time.Now().Add(discardMaxTime)
	buf := make([]byte, 4096)
	for {
		discardTTYInput(tty)
		if !time.Now().Before(end) || tty.SetReadDeadline(time.Now().Add(discardQuietTime)) != nil {
			return
		}
		if n, _ := tty.Read(buf); n == 0 {
			return
		}
	}
}

// parseBuiltQuery checks the query entered in the builder as the run will
// use it: it must be valid UTF-8, parse, and yield the commands of a dmap
// run over the builder's files (checkQuerySpec).
func parseBuiltQuery(current config.Args, query string) (interactiveCommand, error) {
	if !utf8.ValidString(query) {
		return interactiveCommand{}, errors.New("the query is not valid UTF-8")
	}
	if err := checkQuerySpec(current, query); err != nil {
		return interactiveCommand{}, err
	}
	return interactiveCommand{kind: "build", value: query}, nil
}

// parseBuilderFiles parses the :reload of the builder, which takes the
// files the query will read (--files, the only flag of its flag set, see
// newInteractiveReloadFlagSet). No session spec is built: the builder's
// sessions keep holding no workload, only the client's args change.
func parseBuilderFiles(current config.Args, line string) (interactiveCommand, error) {
	next, err := parseReloadLine(current, line)
	if err != nil {
		return interactiveCommand{}, err
	}
	return interactiveCommand{kind: "files", next: next}, nil
}

// runBuildCommand takes the query entered in the builder for the run: it
// hands the query and the files to the builder client (queryBuilt) and
// reports quit, which ends the builder's sessions; the client then runs
// the query as dmap --query does (QueryBuilderClient.Start). Without
// files the query would read nothing, so it is not run and the builder
// stays open with the hint how to name files. Only a line of the builder
// is parsed into this command (handleBuilderLine), and the builder's
// arguments are set by NewQueryBuilderClient alone, which sets queryBuilt
// with them.
func (c *baseClient) runBuildCommand(tty io.Writer, query string) (bool, error) {
	style := styleOf(tty)
	if hint := c.prober.noFilesHint(completesPaths(tty)); hint != "" {
		return false, writeControlLine(tty, style.failure(builderErrorPrefix+"query not run, "+hint))
	}
	args, _ := c.snapshotConnectionState()
	c.profile.queryBuilt(args.What, query)
	return true, writeControlLine(tty, style.success("running the query")+"\n"+
		style.hint("query: ")+style.query(query))
}

// setBuilderFiles applies the :reload --files of the builder: next's files
// are what the built query will read, and the prober is asked for a round
// about them (field probe, path listings). The sessions are not updated.
// As for a :reload of a running workload, journal: files are refused while
// a server whose session is up lacks journal-v1
// (checkReloadJournalCapability), and the files stay as they were: the run
// would fail on that server. A server that is not connected yet is checked
// by the run.
func (c *baseClient) setBuilderFiles(tty io.Writer, next config.Args) error {
	style := styleOf(tty)
	_, spec, connections := c.snapshotMutableState()
	active, _ := partitionReloadConnections(connections)
	if err := checkReloadJournalCapability(active, NewSessionSpec(next)); err != nil {
		return writeControlLine(tty, style.failure(builderErrorPrefix+"files not set: "+errorLines(err)))
	}
	c.storeReloadState(next, spec)
	c.prober.request()
	return writeControlLine(tty, style.success("files set")+"\n"+
		style.hint("files: ")+strconv.Quote(next.What))
}

// writeBuilderState answers :show in the builder: the files the query will
// read and the servers, as there is no workload yet.
func (c *baseClient) writeBuilderState(writer io.Writer) error {
	args, _, connections := c.snapshotMutableState()
	active, _ := partitionReloadConnections(connections)
	return writeControlLine(writer, styleOf(writer).keyValues([]keyValue{
		{key: "mode", value: args.Mode.String()},
		{key: "workload", value: workloadName(args)},
		{key: "files", value: strings.Join(c.sessionFiles(), ",")},
		{key: "query", value: strconv.Quote("")},
		{key: "servers", value: strconv.Itoa(len(connections))},
		{key: "sessions", value: strconv.Itoa(len(active))},
	}))
}

// builderHelpText is the :help text of the query builder, in the layout
// interactiveStyle.help paints (a "Commands:" line, an "e.g.:" line, the
// examples indented by two blanks).
func builderHelpText() string {
	return "Commands: <query>, :query <query>, :reload --files <files>, :show, :fields [table], " +
		":probe, :help, :quit. A line that does not start with a colon is the query.\n" +
		"Tab guides through the query one step at a time (from, the table, select, ...), with the " +
		"tables, fields and values the probe of the servers found in the files; Up/Down recall earlier " +
		"lines; Alt-E (or Alt-V) edits the line in $VISUAL/$EDITOR; --edit-mode vi (or Client.EditMode) " +
		"adds vi normal mode keys after Esc.\n" +
		":fields lists the tables, fields and sample values found (:fields TABLE one table); " +
		":probe probes now.\n" +
		"Build a MapReduce query and press Enter to run it, e.g.:\n" + helpExamples(
		[2]string{"from STATS select count($line) group by $hostname", "a query, typed as it is"},
		[2]string{"from STATS select $hostname,max($goroutines) group by $hostname order by max($goroutines)", ""},
		[2]string{":reload --files /var/log/a.log,/var/log/b.log", "the files to read (Tab lists paths)"},
		[2]string{":quit", "leave without running a query"}) +
		"The query is not quoted. Once it parses, dmap runs it as with --query and exits; started with " +
		"--interactive-query the session stays open and :query replaces the query " +
		"(see doc/interactive-query.md)."
}

// completeBuilderQuery completes the word at the end of before, the line
// of the query builder up to the cursor, as a word of the query the line
// is, with the query completer of :query (completeQueryText). It reports
// false outside the builder and for a line starting with ":" (a control
// command, also where the cursor is still in front of it).
func completeBuilderQuery(unchanged completion, before, after string, args config.Args,
	catalog func() catalogSnapshot) (completion, bool) {

	if !isQueryBuilder(args) {
		return completion{}, false
	}
	query := strings.TrimLeftFunc(before, unicode.IsSpace)
	if strings.HasPrefix(query, ":") || (query == "" && strings.HasPrefix(strings.TrimSpace(after), ":")) {
		return completion{}, false
	}
	return completeQueryText(unchanged, before, query, after, catalog), true
}
