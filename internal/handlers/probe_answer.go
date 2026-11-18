package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/mapr/logformat"
	"github.com/mimecast/dtail/internal/protocol"
)

// run answers request: a path listing (request.List set, runList), or else
// it resolves and samples its files and probes the sample with every
// registered log format (logformat.Names). It never fails: whatever goes
// wrong is an entry of the answer's Errors.
func (p *prober) run(ctx context.Context, request protocol.ProbeRequest) protocol.ProbeAnswer {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if request.List != "" {
		return p.runList(ctx, request)
	}

	r := newProbeRun(p)
	answer := protocol.ProbeAnswer{
		ID:               request.ID,
		DefaultLogFormat: (*mapr.Query)(nil).EffectiveLogFormat(p.defaultLogFormat),
	}
	if len(request.Files) == 0 {
		r.addError("", protocol.ProbeErrorInvalidRequest)
	}
	sample, files, lines := r.sampleTargets(ctx, r.resolveTargets(request.Files))
	answer.FilesSampled = files
	answer.LinesSampled = lines
	if files > 0 {
		answer.Formats = r.probeFormats(sample)
	}
	answer.Errors = r.errors
	return answer
}

// sampleTargets samples every target and returns the lines of all of them,
// each with its path as source id, the number of targets sampled and the number
// of sampled lines. A tail sample's FirstLine is put in front of its lines:
// it is a line of the same file, and header formats (csv) take a source's
// first line as its header. It is not counted as a sampled line, so the
// count is the sampler's (at most 500 lines per file).
func (r *probeRun) sampleTargets(ctx context.Context, targets []probeTarget) (logformat.ProbeSample, int, int) {
	var sample logformat.ProbeSample
	sampled, lineCount := 0, 0
	options := fs.SampleOptions{MaxLineLength: r.maxLineLength}
	for _, target := range targets {
		if ctx.Err() != nil {
			r.addError("", protocol.ProbeErrorTimeout)
			break
		}
		lines, err := r.sample(ctx, target.target, target.path, options)
		if err != nil {
			r.logger.Warn(r.logContext, "Probe: unable to sample file", target.path, err)
			r.addError(target.entry, probeSampleReason(ctx, err))
			continue
		}
		sampled++
		lineCount += len(lines.Lines)
		if lines.HasFirstLine {
			sample.Lines = append(sample.Lines, logformat.ProbeLine{Line: lines.FirstLine, SourceID: target.path})
		}
		for _, line := range lines.Lines {
			sample.Lines = append(sample.Lines, logformat.ProbeLine{Line: line, SourceID: target.path})
		}
	}
	return sample, sampled, lineCount
}

// probeSampleReason is the reason reported for a failed sample. Like every
// probe error it names the request entry, not the file a glob matched; the
// path is in the server log.
func probeSampleReason(ctx context.Context, err error) string {
	if ctx.Err() != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		return protocol.ProbeErrorTimeout
	}
	return protocol.CommandFailureReadingFile
}

// probeFormats probes sample with every registered log format and returns
// the non-empty results. A format whose probe failed is reported by name.
func (r *probeRun) probeFormats(sample logformat.ProbeSample) map[string]protocol.ProbeFormat {
	formats := make(map[string]protocol.ProbeFormat)
	for _, name := range logformat.Names() {
		result, err := logformat.Probe(name, r.hostname, sample)
		if err != nil {
			r.logger.Warn(r.logContext, "Probe: log format probe failed", name, err)
			r.addError(name, protocol.ProbeErrorFormat)
			continue
		}
		if result.Empty() {
			continue
		}
		formats[name] = protocol.ProbeFormat{
			Tables:    result.Tables,
			Variables: result.Variables,
			Note:      result.Note,
		}
	}
	return formats
}

// marshalProbeAnswer returns the JSON of answer, at most maxBytes long when
// that can be had by leaving out sample values and fields: first every sample
// value goes, then as many tables, fields and variables as needed, those of
// the default log format last (probeItems). The answer of a path listing
// loses its last entries instead (marshalFittingEntries). Either sets
// answer.Truncated. Only the errors and counts are always kept; they are
// small (at most protocol.ProbeMaxFiles entries, cut to
// probeMaxErrorFileBytes).
func marshalProbeAnswer(answer protocol.ProbeAnswer, maxBytes int) ([]byte, error) {
	data, err := json.Marshal(answer)
	if err != nil || len(data) <= maxBytes {
		return data, err
	}
	answer.Truncated = true
	if len(answer.Entries) > 0 {
		return marshalFittingEntries(answer, maxBytes)
	}
	formats := withoutSampleValues(answer.Formats)
	answer.Formats = formats
	if data, err = json.Marshal(answer); err != nil || len(data) <= maxBytes {
		return data, err
	}
	return marshalFittingItems(answer, formats, maxBytes)
}

// marshalFittingItems searches for the largest number of items
// (probeItems) of formats whose answer fits maxBytes and returns that
// answer's JSON. Answers grow with the number of items, so a binary search
// needs about log2(items) encodings.
func marshalFittingItems(answer protocol.ProbeAnswer, formats map[string]protocol.ProbeFormat,
	maxBytes int) ([]byte, error) {

	items := probeItems(formats, answer.DefaultLogFormat)
	low, high := 0, len(items)-1 // the answer with all items does not fit
	answer.Formats = keepProbeItems(items, 0)
	best, err := json.Marshal(answer)
	if err != nil {
		return nil, err
	}
	for low < high {
		mid := (low + high + 1) / 2
		answer.Formats = keepProbeItems(items, mid)
		data, err := json.Marshal(answer)
		if err != nil {
			return nil, err
		}
		if len(data) <= maxBytes {
			low, best = mid, data
		} else {
			high = mid - 1
		}
	}
	return best, nil
}

// marshalFittingEntries searches for the largest number of the first
// entries of a path listing answer that fits maxBytes and returns that
// answer's JSON, like marshalFittingItems.
func marshalFittingEntries(answer protocol.ProbeAnswer, maxBytes int) ([]byte, error) {
	entries := answer.Entries
	low, high := 0, len(entries)-1 // the answer with all entries does not fit
	answer.Entries = nil
	best, err := json.Marshal(answer)
	if err != nil {
		return nil, err
	}
	for low < high {
		mid := (low + high + 1) / 2
		answer.Entries = entries[:mid]
		data, err := json.Marshal(answer)
		if err != nil {
			return nil, err
		}
		if len(data) <= maxBytes {
			low, best = mid, data
		} else {
			high = mid - 1
		}
	}
	return best, nil
}

// probeItem is one table, field or variable of a format.
type probeItem struct {
	format, table, field, variable string
	kind                           probeItemKind
}

type probeItemKind int

const (
	probeItemTable probeItemKind = iota
	probeItemField
	probeItemVariable
)

// probeItems lists the tables (each followed by its fields) and variables of
// formats, the default log format first, then the others by name, and
// tables, fields and variables sorted, so the items kept of a cut answer do
// not depend on map order.
func probeItems(formats map[string]protocol.ProbeFormat, defaultFormat string) []probeItem {
	names := sortedKeys(formats)
	if i := slices.Index(names, defaultFormat); i > 0 {
		names = slices.Insert(slices.Delete(names, i, i+1), 0, defaultFormat)
	}
	var items []probeItem
	for _, name := range names {
		format := formats[name]
		for _, table := range sortedKeys(format.Tables) {
			items = append(items, probeItem{format: name, table: table, kind: probeItemTable})
			for _, field := range sortedKeys(format.Tables[table]) {
				items = append(items, probeItem{format: name, table: table, field: field, kind: probeItemField})
			}
		}
		for _, variable := range sortedKeys(format.Variables) {
			items = append(items, probeItem{format: name, variable: variable, kind: probeItemVariable})
		}
	}
	return items
}

// keepProbeItems builds the formats of the first n items, without sample
// values (and without notes, which are cut with the values).
func keepProbeItems(items []probeItem, n int) map[string]protocol.ProbeFormat {
	formats := make(map[string]protocol.ProbeFormat)
	for _, item := range items[:n] {
		format := formats[item.format]
		switch item.kind {
		case probeItemTable:
			if format.Tables == nil {
				format.Tables = make(map[string]map[string][]string)
			}
			format.Tables[item.table] = make(map[string][]string)
		case probeItemField:
			format.Tables[item.table][item.field] = []string{}
		case probeItemVariable:
			if format.Variables == nil {
				format.Variables = make(map[string][]string)
			}
			format.Variables[item.variable] = []string{}
		}
		formats[item.format] = format
	}
	return formats
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// withoutSampleValues returns a copy of formats with every field and
// variable kept but no sample values and no notes.
func withoutSampleValues(formats map[string]protocol.ProbeFormat) map[string]protocol.ProbeFormat {
	items := probeItems(formats, "")
	return keepProbeItems(items, len(items))
}
