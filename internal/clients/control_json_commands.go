package clients

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mimecast/dtail/internal/config"
)

// controlCommand is one command of the JSON control channel.
type controlCommand struct {
	name string
	// about says what the command does and which request fields it reads,
	// for the help command.
	about string
	// available reports whether a client with these arguments has the
	// command; nil for every client.
	available func(args config.Args) bool
	run       func(s *controlSession, ctx context.Context, request controlRequest) (any, error)
}

// controlCommands are the commands, in the order help lists them. quit is
// handled by the loop itself (controlSession.handleLine).
func controlCommands() []controlCommand {
	return []controlCommand{
		{name: "help", about: "the commands of this session and the help text of the interactive mode",
			run: (*controlSession).help},
		{name: "show", about: "the session's state: workload, files, query, regex, servers, sessions",
			run: (*controlSession).show},
		{name: "fields", about: "tables, fields, sample values and $variables the servers found in the files; " +
			"\"table\": probe that table on its own and return only it", run: (*controlSession).fields},
		{name: "probe", about: "probe the servers now and report which answered",
			run: (*controlSession).probe},
		{name: "list", about: "the files and directories on the server starting with \"prefix\" " +
			"(journal: lists the readable units)", run: (*controlSession).list},
		{name: "complete", about: "what Tab would do: complete \"query\" (a query typed so far) or \"line\" " +
			"(a control line such as \":reload --files /var/l\") at \"cursor\" (default: the end)",
			run: (*controlSession).complete},
		{name: "query", about: "run the MapReduce query \"query\"; without one, return the query that runs",
			available: hasQueryCommand, run: (*controlSession).query},
		{name: "tail", about: "go back to following lines, with \"regex\" or the regex followed before",
			available: func(args config.Args) bool { return hasTailCommand(args) && !isQueryBuilder(args) },
			run:       (*controlSession).tail},
		{name: "reload", about: "change the workload with \"args\", the client's flags one argument each, " +
			"e.g. [\"--files\",\"/var/log/a.log\"]", run: (*controlSession).reload},
		{name: "pause", about: "stop the output (the servers are held back, nothing is lost)",
			run: (*controlSession).pauseOutput},
		{name: "resume", about: "let the output run again", run: (*controlSession).resumeOutput},
	}
}

// controlCommandNames lists the commands a client with args has, with quit.
func controlCommandNames(args config.Args) []string {
	var names []string
	for _, command := range controlCommands() {
		if command.available == nil || command.available(args) {
			names = append(names, command.name)
		}
	}
	return append(names, "quit")
}

// run runs one request other than quit and returns its result.
//
// The commands whose answer a server sends (reload, query, tail, probe)
// resume a paused output once their request was accepted (in their
// handlers), as the control TTY does (resumesBeforeRun): the handlers
// that read the answer are blocked while the output is paused. A rejected
// request leaves the pause as it is.
func (s *controlSession) run(ctx context.Context, request controlRequest) (any, error) {
	args, _ := s.client.snapshotConnectionState()
	for _, command := range controlCommands() {
		if command.name != request.Cmd {
			continue
		}
		if command.available != nil && !command.available(args) {
			return nil, fmt.Errorf("this session has no %q command; it has: %s", request.Cmd,
				strings.Join(controlCommandNames(args), ", "))
		}
		return command.run(s, ctx, request)
	}
	if request.Cmd == "" {
		return nil, errors.New("the request has no \"cmd\"; send {\"cmd\":\"help\"} for the commands")
	}
	return nil, fmt.Errorf("unknown command %q; the commands are: %s", request.Cmd,
		strings.Join(controlCommandNames(args), ", "))
}

// controlHelp is the result of help.
type controlHelp struct {
	Commands map[string]string `json:"commands"`
	// Text is the help of the interactive mode as the control TTY prints
	// it, with examples of the flags and queries of the client's mode. Its
	// ":reload <flags>" lines are reload requests with those flags as args.
	Text string `json:"text"`
}

func (s *controlSession) help(context.Context, controlRequest) (any, error) {
	args, _ := s.client.snapshotConnectionState()
	help := controlHelp{Commands: map[string]string{"quit": "end the session"}}
	for _, command := range controlCommands() {
		if command.available == nil || command.available(args) {
			help.Commands[command.name] = command.about
		}
	}
	if isQueryBuilder(args) {
		help.Text = builderHelpText()
	} else {
		help.Text = interactiveHelpText(args)
	}
	return help, nil
}

// controlState is the result of show.
type controlState struct {
	Mode     string   `json:"mode"`
	Workload string   `json:"workload"`
	Files    []string `json:"files"`
	Query    string   `json:"query,omitempty"`
	Regex    string   `json:"regex"`
	Invert   bool     `json:"invert"`
	Options  string   `json:"options,omitempty"`
	Timeout  int      `json:"timeout"`
	Servers  int      `json:"servers"`
	Sessions int      `json:"sessions"`
	Paused   bool     `json:"paused"`
}

func (s *controlSession) show(context.Context, controlRequest) (any, error) {
	c := s.client
	args, spec, connections := c.snapshotMutableState()
	active, _ := partitionReloadConnections(connections)
	return controlState{
		Mode:     args.Mode.String(),
		Workload: workloadName(args),
		Files:    nonNilStrings(c.sessionFiles()),
		Query:    spec.Query,
		Regex:    spec.Regex,
		Invert:   spec.RegexInvert,
		Options:  spec.Options,
		Timeout:  spec.Timeout,
		Servers:  len(connections),
		Sessions: len(active),
		Paused:   s.pause.paused,
	}, nil
}

// controlPaused is the result of pause and resume.
type controlPaused struct {
	Paused bool `json:"paused"`
}

func (s *controlSession) pauseOutput(context.Context, controlRequest) (any, error) {
	s.pause.pause()
	return controlPaused{Paused: true}, nil
}

func (s *controlSession) resumeOutput(context.Context, controlRequest) (any, error) {
	s.pause.resume()
	return controlPaused{}, nil
}

// controlApplied is the result of reload, query and tail: what the session
// runs now and which servers were not switched.
type controlApplied struct {
	Workload string   `json:"workload"`
	Files    []string `json:"files"`
	Query    string   `json:"query,omitempty"`
	Regex    string   `json:"regex,omitempty"`
	Invert   bool     `json:"invert,omitempty"`
	// Connecting are the servers still connecting, which start with the
	// new workload, and Inactive those without a session, which were not
	// switched.
	Connecting []string `json:"connecting,omitempty"`
	Inactive   []string `json:"inactive,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

// applied describes the session after outcome.
func (s *controlSession) applied(outcome reloadOutcome) controlApplied {
	args, spec := s.client.snapshotConnectionState()
	return controlApplied{
		Workload:   workloadName(args),
		Files:      nonNilStrings(s.client.sessionFiles()),
		Query:      spec.Query,
		Regex:      spec.Regex,
		Invert:     spec.RegexInvert,
		Connecting: outcome.connecting,
		Inactive:   outcome.inactive,
		Notes:      outcome.notes,
	}
}

// reload applies the flags of the request like a :reload line does, without
// its tokenizer: every argument is one flag or value as given. In the
// session of dmap that holds no workload the only flag is --files, which
// sets the files the queries read (setSessionFiles).
func (s *controlSession) reload(_ context.Context, request controlRequest) (any, error) {
	c := s.client
	if len(request.Args) == 0 {
		return nil, errors.New("reload requires \"args\", the flags to change, e.g. [\"--files\",\"/var/log/a.log\"]")
	}
	if slices.ContainsFunc(request.Args, func(arg string) bool { return !utf8.ValidString(arg) }) {
		return nil, errors.New("the reload args are not valid UTF-8")
	}
	current, _ := c.snapshotConnectionState()
	next, err := parseInteractiveReloadArgs(current, request.Args)
	if err != nil {
		return nil, err
	}
	if isQueryBuilder(current) {
		if err = c.setSessionFiles(next); err != nil {
			return nil, err
		}
		return s.applied(reloadOutcome{}), nil
	}
	next, spec, err := buildInteractiveSessionSpec(next)
	if err != nil {
		return nil, err
	}
	s.pause.resume()
	outcome, err := c.applyInteractiveReload(next, spec)
	if err != nil {
		return nil, err
	}
	return s.applied(outcome), nil
}

// tail switches interactive dtail back to following lines (switchToTail).
func (s *controlSession) tail(_ context.Context, request controlRequest) (any, error) {
	current, _ := s.client.snapshotConnectionState()
	command, err := parseTailCommand(current, tailCommand+" "+request.Regex)
	if err != nil {
		return nil, err
	}
	s.pause.resume()
	outcome, err := s.client.switchToTail(command.value)
	if err != nil {
		return nil, err
	}
	return s.applied(outcome), nil
}

// query runs the query of the request. A session with a workload switches
// to it (interactive dtail) or replaces its query (interactive dmap), and
// the results follow as result events while the session runs it. The
// session of dmap that holds no workload runs the query to its end and
// answers with the final result (runHeldQuery). Without a query the result
// is the state, which names the query that runs.
func (s *controlSession) query(ctx context.Context, request controlRequest) (any, error) {
	c := s.client
	current, _ := c.snapshotConnectionState()
	if strings.TrimSpace(request.Query) == "" {
		return s.show(ctx, request)
	}
	if isQueryBuilder(current) {
		return s.runHeldQuery(ctx, current, request)
	}
	command, err := parseQueryCommand(current, queryCommand+" "+request.Query)
	if err != nil {
		return nil, err
	}
	s.pause.resume()
	outcome, err := c.applyQueryCommand(command.value)
	if err != nil {
		return nil, err
	}
	return s.applied(outcome), nil
}

// runHeldQuery runs the query of the request over the session's files on
// connections of its own (clientProfile.runQuery) and returns its final
// result. Progress of the read is written as progress events with the
// request's id meanwhile.
func (s *controlSession) runHeldQuery(ctx context.Context, current config.Args, request controlRequest) (any, error) {
	c := s.client
	command, err := parseBuiltQuery(current, strings.TrimSpace(request.Query))
	if err != nil {
		return nil, err
	}
	if c.prober.readsNoFiles() {
		return nil, errors.New(controlNoFiles)
	}
	if c.profile.runQuery == nil {
		return nil, errNoQuerySession
	}
	s.pause.resume()
	progress := func(report progressReport) {
		c.control.write(newControlProgress(request.ID, report))
	}
	return c.profile.runQuery(ctx, current.What, command.value, progress)
}

// controlNoFiles is the error of a request that needs files in a session
// that reads none.
const controlNoFiles = "the session reads no files: send {\"cmd\":\"reload\",\"args\":[\"--files\",\"PATH\"]} " +
	"(the list command lists the paths on the server)"

// controlProgress is the progress event of a query run: how far the servers
// read the files (see progressReport).
type controlProgress struct {
	Event string `json:"event"`
	ID    any    `json:"id,omitempty"`
	Bytes int64  `json:"bytes"`
	Total int64  `json:"total"`
	// Percent has one decimal.
	Percent    float64 `json:"percent"`
	Files      int64   `json:"files"`
	FilesTotal int64   `json:"filesTotal"`
	Reporting  int     `json:"reporting"`
	Servers    int     `json:"servers"`
	// BytesPerSecond is 0 and ETA "unknown" until a rate is known.
	BytesPerSecond int64  `json:"bytesPerSecond"`
	ETA            string `json:"eta"`
}

func newControlProgress(id any, report progressReport) controlProgress {
	return controlProgress{
		Event:          "progress",
		ID:             id,
		Bytes:          report.Bytes,
		Total:          report.Total,
		Percent:        math.Round(report.percent()*10) / 10,
		Files:          report.Files,
		FilesTotal:     report.FilesTotal,
		Reporting:      report.Reporting,
		Servers:        report.Servers,
		BytesPerSecond: int64(report.Rate),
		ETA:            report.eta(),
	}
}

// waitHeld waits for done, at most timeout and until ctx ends, with paused
// output held meanwhile (interactivePause.holdOutput): a server's answer is
// read by the handlers a pause blocks. It reports whether done was closed.
func (s *controlSession) waitHeld(ctx context.Context, done <-chan struct{}, timeout time.Duration) bool {
	if done == nil {
		return false
	}
	var arrived bool
	s.pause.holdOutput(func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-done:
			arrived = true
		case <-timer.C:
		case <-ctx.Done():
		}
	})
	return arrived
}
