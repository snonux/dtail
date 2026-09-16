package logformat

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mimecast/dtail/internal/protocol"
)

// csvParser parses CSV log lines. The first line encountered for a given
// sourceID is treated as the column header and stored so that subsequent
// lines from the same source can be mapped to named fields. State is kept
// per sourceID because a single parser instance is shared across every
// file/stream processed within a mapreduce session; without this, the
// header row of every file after the first one would silently be mapped
// as a data row, corrupting aggregates.
type csvParser struct {
	defaultParser
	mu      sync.RWMutex
	headers map[string][]string
}

var _ Parser = (*csvParser)(nil)
var _ FieldsIntoParser = (*csvParser)(nil)

func newCSVParser(hostname, timeZoneName string, timeZoneOffset int) (*csvParser, error) {
	defaultParser, err := newDefaultParser(hostname, timeZoneName, timeZoneOffset)
	if err != nil {
		return &csvParser{}, err
	}
	return &csvParser{
		defaultParser: *defaultParser,
		headers:       make(map[string][]string),
	}, nil
}

func (p *csvParser) MakeFields(maprLine, sourceID string) (map[string]string, error) {
	fields := make(map[string]string, p.fieldsCapacity)
	if err := p.MakeFieldsInto(fields, maprLine, sourceID); err != nil {
		if errors.Is(err, ErrIgnoreFields) {
			return nil, err
		}
		return fields, err
	}
	return fields, nil
}

// MakeFieldsInto must be defined here rather than inherited from the embedded
// defaultParser: the promoted method would parse the line in DTail's own
// MAPREDUCE layout instead of the CSV one.
func (p *csvParser) MakeFieldsInto(dst map[string]string, maprLine, sourceID string) error {
	clear(dst)
	header, installed := p.ensureHeader(sourceID, maprLine)
	if installed {
		return ErrIgnoreFields
	}

	p.addDefaultFields(dst, maprLine)
	start := 0
	column := 0
	delimiter := protocol.CSVDelimiter[0]

	for {
		value, next, done := scanDelimitedField(maprLine, start, delimiter)
		if column >= len(header) {
			return fmt.Errorf("CSV file seems corrupted, more fields than header values?")
		}
		p.addDynamicField(dst, header[column], value)
		column++
		if done {
			break
		}
		start = next
	}

	return nil
}

// ensureHeader atomically checks for, and if necessary installs, the header
// for sourceID. It returns the effective header for the source and whether
// this call was the one that installed it. Only the goroutine that actually
// installs the header should tell its caller to ignore the current line
// (i.e. return ErrIgnoreFields); any racing goroutine on the same sourceID
// sees installed=false and proceeds to map its line against the installed
// header. The previous implementation split the check (RLock) from the
// install (Lock), so two goroutines could both observe "missing" and both
// report ErrIgnoreFields, silently dropping the loser's data row.
func (p *csvParser) ensureHeader(sourceID, maprLine string) ([]string, bool) {
	p.mu.RLock()
	if header, ok := p.headers[sourceID]; ok {
		p.mu.RUnlock()
		return header, false
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if header, ok := p.headers[sourceID]; ok {
		return header, false
	}
	header := parseHeaderLine(maprLine)
	p.headers[sourceID] = header
	return header, true
}

// parseHeaderLine copies every column name out of maprLine. The header is kept
// for the whole session while maprLine is only borrowed for the duration of
// the parse call (see Parser.MakeFields), so storing sub-slices of it would
// leave the header pointing at whatever the caller writes into that buffer
// next.
func parseHeaderLine(maprLine string) []string {
	var header []string
	start := 0
	delimiter := protocol.CSVDelimiter[0]
	for {
		field, next, done := scanDelimitedField(maprLine, start, delimiter)
		header = append(header, strings.Clone(field))
		if done {
			break
		}
		start = next
	}
	return header
}
