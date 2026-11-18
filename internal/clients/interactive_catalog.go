package clients

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/protocol"
)

// catalogMaxSamples is how many distinct sample values the merged catalog
// keeps per field or $variable, over all servers (a server sends up to 5).
const catalogMaxSamples = 5

// fieldCatalog caches what the background prober (interactiveProber)
// learned about the MapReduce tables, fields and $variables of the files the
// interactive session reads, for the query completion: Tab cannot wait for a
// server (see interactiveProber), so it only ever reads this cache. It keeps
// the latest probe answer of every server with the files it was asked about
// and merges, on read (snapshot), the answers about the session's current
// files: a server's new answer replaces what it said before, and after a
// :reload to other files the answers about the old ones no longer count,
// also those of servers that are not asked again (more than
// probeMaxConnections servers, a reconnect, a failed probe). Back on those
// files, they count again. It is safe for concurrent use: the prober
// stores, the control loop reads.
//
// It also caches the path listings of the --files completion per request
// (storeListing, findListing in interactive_catalog_paths.go), which are
// one server's view and not merged.
type fieldCatalog struct {
	mu       sync.Mutex
	answers  map[string]catalogAnswer
	listings map[string]pathListing
}

// catalogAnswer is the latest probe answer of one server, the files it
// answered about (the request's entries, sorted) and when it arrived.
type catalogAnswer struct {
	answer   protocol.ProbeAnswer
	files    []string
	received time.Time
}

// catalogSnapshot is the catalog merged over all servers that answered.
type catalogSnapshot struct {
	// DefaultLogFormat is the log format of a query with a table and
	// without a logformat clause: the one most servers named. On a tie the
	// format that reached the top count first, going through Servers in
	// order, wins (a: csv, b: default gives csv; a: csv, b: default, c:
	// default, d: csv gives default).
	DefaultLogFormat string
	// Formats maps every log format any server found fields for to the
	// union of what the servers found.
	Formats map[string]catalogFormat
	// Servers are the servers whose answers make up the snapshot (those
	// about the files asked for), sorted.
	Servers []string
	// Updated is when the latest of these answers arrived; zero for an empty
	// catalog.
	Updated time.Time
	// Truncated is set when any server cut its answer to the size limit.
	Truncated bool
	// Errors are the errors of the servers' answers.
	Errors []catalogError
}

// catalogFormat is what the servers found for one log format: tables
// (MAPREDUCE:<TABLE> of the default format, "" for formats without tables)
// mapping fields to sample values, $variables with sample values, and the
// distinct notes of custom probes.
type catalogFormat struct {
	Tables    map[string]map[string][]string
	Variables map[string][]string
	Notes     []string
}

// catalogError is an error entry of a server's probe answer.
type catalogError struct {
	Server string
	File   string
	Error  string
}

func newFieldCatalog() *fieldCatalog {
	return &fieldCatalog{answers: make(map[string]catalogAnswer), listings: make(map[string]pathListing)}
}

// store records answer as server's latest one, to a probe of files (the
// request's entries), received at the given time. An answer that only says
// that another probe of the session was running (protocol.ProbeErrorBusy)
// carries nothing about the files and is ignored, so it does not wipe the
// server's previous answer.
func (c *fieldCatalog) store(server string, files []string, answer protocol.ProbeAnswer, received time.Time) {
	if isBusyProbeAnswer(answer) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answers[server] = catalogAnswer{answer: answer, files: slices.Sorted(slices.Values(files)),
		received: received}
}

// snapshot returns the catalog merged over the servers whose latest answer
// is about files (probe request entries, in any order). It shares no map or
// slice with the catalog, so the caller may keep and change it.
func (c *fieldCatalog) snapshot(files []string) catalogSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	wanted := slices.Sorted(slices.Values(files))
	snap := catalogSnapshot{Formats: make(map[string]catalogFormat)}
	for _, server := range slices.Sorted(maps.Keys(c.answers)) {
		if slices.Equal(c.answers[server].files, wanted) {
			snap.Servers = append(snap.Servers, server)
		}
	}
	defaults := make(map[string]int)
	for _, server := range snap.Servers {
		entry := c.answers[server]
		if entry.received.After(snap.Updated) {
			snap.Updated = entry.received
		}
		snap.Truncated = snap.Truncated || entry.answer.Truncated
		if format := entry.answer.DefaultLogFormat; format != "" {
			defaults[format]++
			if defaults[format] > defaults[snap.DefaultLogFormat] {
				snap.DefaultLogFormat = format
			}
		}
		for name, format := range entry.answer.Formats {
			snap.Formats[name] = mergeCatalogFormat(snap.Formats[name], format)
		}
		for _, probeErr := range entry.answer.Errors {
			snap.Errors = append(snap.Errors, catalogError{Server: server, File: probeErr.File, Error: probeErr.Error})
		}
	}
	return snap
}

// isBusyProbeAnswer reports whether answer only says that another probe of
// the session was running.
func isBusyProbeAnswer(answer protocol.ProbeAnswer) bool {
	if len(answer.Errors) == 0 || answer.FilesSampled > 0 || len(answer.Formats) > 0 {
		return false
	}
	for _, probeErr := range answer.Errors {
		if probeErr.Error != protocol.ProbeErrorBusy {
			return false
		}
	}
	return true
}

// mergeCatalogFormat returns merged with what format adds; merged may be the
// zero value.
func mergeCatalogFormat(merged catalogFormat, format protocol.ProbeFormat) catalogFormat {
	if merged.Tables == nil && len(format.Tables) > 0 {
		merged.Tables = make(map[string]map[string][]string)
	}
	for table, fields := range format.Tables {
		merged.Tables[table] = mergeCatalogSamples(merged.Tables[table], fields)
	}
	if len(format.Variables) > 0 {
		merged.Variables = mergeCatalogSamples(merged.Variables, format.Variables)
	}
	if format.Note != "" && !slices.Contains(merged.Notes, format.Note) {
		merged.Notes = append(merged.Notes, format.Note)
	}
	return merged
}

// mergeCatalogSamples adds the names of from with their sample values to
// into (allocated when nil) and returns it. A name's samples are the
// distinct values in order of appearance, at most catalogMaxSamples.
func mergeCatalogSamples(into map[string][]string, from map[string][]string) map[string][]string {
	if into == nil {
		into = make(map[string][]string, len(from))
	}
	for name, samples := range from {
		merged, seen := into[name]
		if !seen {
			// An empty, non-nil list records the name without samples.
			merged = make([]string, 0, min(len(samples), catalogMaxSamples))
		}
		for _, sample := range samples {
			if len(merged) >= catalogMaxSamples {
				break
			}
			if !slices.Contains(merged, sample) {
				merged = append(merged, sample)
			}
		}
		into[name] = merged
	}
	return into
}
