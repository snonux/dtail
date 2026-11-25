package clients

import (
	"context"
	"errors"
	"strings"
	"time"
)

// The requests of the JSON control channel that read what the servers'
// probes found: fields, probe, list and complete. They answer from the
// background prober's catalog, as :fields, :probe and Tab do, but wait for
// the probe they need (controlProbeWait) instead of telling to ask again.

// controlFields is the result of fields: the catalog of the session's files
// merged over the servers that answered.
type controlFields struct {
	// DefaultLogFormat is the log format of a query with a table and
	// without a logformat clause.
	DefaultLogFormat string `json:"defaultLogFormat,omitempty"`
	// Formats maps a log format to what the servers found for it.
	Formats map[string]controlFormat `json:"formats"`
	// Servers are the servers that answered, AgeSeconds how old their
	// latest answer is.
	Servers    []string `json:"servers"`
	AgeSeconds float64  `json:"ageSeconds"`
	// Truncated is set when a server cut its answer to its size limit.
	Truncated bool `json:"truncated,omitempty"`
	// Errors are the servers' probe errors, e.g. a file that is not
	// readable.
	Errors []controlProbeError `json:"errors,omitempty"`
	// TableProbes maps every table probed on its own to the lines of it
	// the servers sampled (0: the table was not found).
	TableProbes map[string]int `json:"tableProbes,omitempty"`
	// Note says why the catalog is empty, if it is.
	Note string `json:"note,omitempty"`
}

// controlFormat is one log format of controlFields. The table "" holds the
// fields of lines without a table a query can name: a query without a from
// clause reads them.
type controlFormat struct {
	Tables map[string]controlTable `json:"tables"`
	// Variables are the $variables of the format with sample values; left
	// out when the fields of one table were asked for (see controlTable).
	Variables map[string][]string `json:"variables,omitempty"`
	Notes     []string            `json:"notes,omitempty"`
}

// controlTable is one table: its fields and the $variables its lines
// filled, each with sample values.
type controlTable struct {
	Fields    map[string][]string `json:"fields"`
	Variables map[string][]string `json:"variables,omitempty"`
}

// controlProbeError is one error of a server's probe answer.
type controlProbeError struct {
	Server string `json:"server"`
	File   string `json:"file,omitempty"`
	Error  string `json:"error"`
}

// fields answers with the catalog. With a table it first asks the servers
// for a table probe of it (as :fields TABLE does) and returns that table
// only; without one it probes first when no server answered yet. Both
// waits end after controlProbeWait at the latest.
func (s *controlSession) fields(ctx context.Context, request controlRequest) (any, error) {
	prober := s.client.prober
	if prober.readsNoFiles() {
		return nil, errors.New(controlNoFiles)
	}
	table := strings.TrimSpace(request.Table)
	snap := prober.catalogSnapshot()
	if len(snap.Servers) == 0 {
		s.pause.holdOutput(func() { prober.probeNow() })
		snap = prober.catalogSnapshot()
	}
	if table != "" {
		done, _ := prober.requestTable(catalogTableName(snap, table))
		s.waitHeld(ctx, done, controlProbeWait)
		snap = prober.catalogSnapshot()
	}
	return newControlFields(snap, table, prober.now()), nil
}

// newControlFields turns snap into the result of fields, narrowed to table
// (matched ignoring case) when one is given.
func newControlFields(snap catalogSnapshot, table string, now time.Time) controlFields {
	result := controlFields{
		DefaultLogFormat: snap.DefaultLogFormat,
		Formats:          make(map[string]controlFormat),
		Servers:          nonNilStrings(snap.Servers),
		Truncated:        snap.Truncated,
		TableProbes:      snap.TableProbes,
	}
	if !snap.Updated.IsZero() {
		result.AgeSeconds = now.Sub(snap.Updated).Round(time.Millisecond).Seconds()
	}
	for _, probeErr := range snap.Errors {
		result.Errors = append(result.Errors, controlProbeError(probeErr))
	}
	for name, format := range snap.Formats {
		if converted, ok := newControlFormat(format, table); ok {
			result.Formats[name] = converted
		}
	}
	switch {
	case len(snap.Servers) == 0:
		result.Note = "no server answered the probe yet"
	case len(result.Formats) == 0 && table != "":
		result.Note = "no line of table " + table + " was found in the sampled part of the files"
	case len(result.Formats) == 0:
		result.Note = "no fields were found in the sampled part of the files"
	}
	return result
}

// newControlFormat converts format, keeping only table when one is given;
// it reports false when nothing of the format is left then.
func newControlFormat(format catalogFormat, table string) (controlFormat, bool) {
	result := controlFormat{Tables: make(map[string]controlTable), Notes: format.Notes}
	for name, fields := range format.Tables {
		if table != "" && !strings.EqualFold(name, table) {
			continue
		}
		result.Tables[name] = controlTable{Fields: nonNilSamples(fields), Variables: format.variablesOf(name)}
	}
	if table != "" {
		return result, len(result.Tables) > 0
	}
	result.Variables = format.Variables
	return result, true
}

// nonNilSamples returns samples, or an empty map for nil, so that JSON has
// {} instead of null.
func nonNilSamples(samples map[string][]string) map[string][]string {
	if samples == nil {
		return map[string][]string{}
	}
	return samples
}

// controlProbeResult is the result of probe: how the round ended ("done",
// "timeout": no round finished in time, its answers are stored when they
// arrive, or "stopped": the session is ending) and what the servers did.
type controlProbeResult struct {
	Status   string   `json:"status"`
	Files    int      `json:"files"`
	Answered []string `json:"answered"`
	Busy     []string `json:"busy,omitempty"`
	Failed   []string `json:"failed,omitempty"`
}

// probe requests a probe round now and waits for it (probeNow).
func (s *controlSession) probe(context.Context, controlRequest) (any, error) {
	prober := s.client.prober
	if prober.readsNoFiles() {
		return nil, errors.New(controlNoFiles)
	}
	s.pause.resume()
	round, status := prober.probeNow()
	result := controlProbeResult{Files: round.files, Answered: nonNilStrings(round.answered),
		Busy: round.busy, Failed: round.failed}
	switch status {
	case probeWaitDone:
		result.Status = "done"
	case probeWaitTimeout:
		result.Status = "timeout"
	default:
		result.Status = "stopped"
	}
	return result, nil
}

// controlListing is the result of list: the names in Dir starting with the
// rest of the prefix, as one server sees them. Only what the user may read
// is listed, so a missing, unreadable or forbidden directory is an empty
// listing.
type controlListing struct {
	Dir     string             `json:"dir"`
	Entries []controlListEntry `json:"entries"`
	Server  string             `json:"server,omitempty"`
	// Truncated is set when the server cut the listing: ask with a longer
	// prefix.
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// controlListEntry is one listed name; Path is what a --files value takes.
type controlListEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
}

// list answers with the listing of the request's prefix (a path up to its
// last "/" names the directory, the rest the start of the names; journal:
// lists units), from the catalog's cache or, when that has none that
// covers the prefix, from the server, waited for up to controlProbeWait.
func (s *controlSession) list(ctx context.Context, request controlRequest) (any, error) {
	prober := s.client.prober
	if prober == nil {
		return nil, errors.New("the session has no prober")
	}
	dir, base := listingDirKey(request.Prefix)
	match := prober.catalog.findListing(dir, base, prober.now())
	if !match.found || !match.complete {
		fetch := match.fetch
		if fetch == "" {
			fetch = request.Prefix
		}
		if !s.waitHeld(ctx, prober.listPaths(fetch), controlProbeWait) && !match.found {
			return nil, errors.New("no server answered the listing in time; is a server connected?")
		}
		match = prober.catalog.findListing(dir, base, prober.now())
	}
	if !match.found {
		return nil, errors.New("no server answered the listing")
	}
	return newControlListing(request.Prefix, base, match.listing), nil
}

// newControlListing turns listing into the result of list for prefix, whose
// names start with base.
func newControlListing(prefix, base string, listing pathListing) controlListing {
	dir := strings.TrimSuffix(prefix, base)
	result := controlListing{Dir: dir, Entries: []controlListEntry{}, Server: listing.server,
		Truncated: listing.truncated, Error: listing.err}
	for _, entry := range listing.entries {
		if !strings.HasPrefix(entry.Name, base) {
			continue
		}
		result.Entries = append(result.Entries, controlListEntry{Name: entry.Name, Path: dir + entry.Name, Dir: entry.Dir})
	}
	return result
}

// controlCompletion is the result of complete: what Tab would do to the
// line. Line and Cursor are the line after the completion (unchanged when
// Changed is false); Candidates are what could follow at the cursor, also
// grouped by what they are (Groups: table, field, aggregation, ...) under
// Heading, which says what is expected there. Hint is a note such as that
// a comma adds another item.
type controlCompletion struct {
	Line       string                `json:"line"`
	Cursor     int                   `json:"cursor"`
	Changed    bool                  `json:"changed"`
	Heading    string                `json:"heading,omitempty"`
	Candidates []string              `json:"candidates"`
	Groups     []controlCandidateSet `json:"groups,omitempty"`
	Hint       string                `json:"hint,omitempty"`
}

// controlCandidateSet is the candidates of one kind.
type controlCandidateSet struct {
	Label      string   `json:"label"`
	Candidates []string `json:"candidates"`
}

// complete completes the request's query, or its control line, like Tab in
// the line editor (completeEditorLine: commands, flags, paths inside
// --files, and the guided query completion with the tables and fields of
// the catalog, waiting for a path listing or table probe it needs). A
// query is completed as the line of the session that takes one: after
// ":query " or, in the session of dmap that holds no workload, as the line
// itself; the result's line is the query again.
func (s *controlSession) complete(_ context.Context, request controlRequest) (any, error) {
	c := s.client
	line, prefix := request.Line, ""
	if request.Query != "" || line == "" {
		args, _ := c.snapshotConnectionState()
		if !isQueryBuilder(args) {
			prefix = queryCommand + " "
		}
		line = prefix + request.Query
	}
	cursor := len(line)
	if request.Cursor != nil {
		cursor = *request.Cursor + len(prefix)
	}
	if cursor < len(prefix) || cursor > len(line) {
		return nil, errors.New("the cursor is outside the line")
	}
	done := c.completeEditorLine(s.pause, line, cursor)
	result := controlCompletion{Line: strings.TrimPrefix(done.line, prefix), Cursor: done.pos - len(prefix),
		Changed: done.changed, Heading: done.heading, Candidates: nonNilStrings(done.candidates), Hint: done.hint}
	for _, group := range done.groups {
		result.Groups = append(result.Groups, controlCandidateSet{Label: group.label, Candidates: group.candidates})
	}
	return result, nil
}
