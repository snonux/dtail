package logformat

import (
	"fmt"
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
	header, ok := p.headerFor(sourceID)
	if !ok {
		p.parseHeader(sourceID, maprLine)
		return nil, ErrIgnoreFields
	}

	fields := make(map[string]string, p.fieldsCapacity)
	p.addDefaultFields(fields, maprLine)
	start := 0
	column := 0
	delimiter := protocol.CSVDelimiter[0]

	for {
		value, next, done := scanDelimitedField(maprLine, start, delimiter)
		if column >= len(header) {
			return fields, fmt.Errorf("CSV file seems corrupted, more fields than header values?")
		}
		p.addDynamicField(fields, header[column], value)
		column++
		if done {
			break
		}
		start = next
	}

	return fields, nil
}

func (p *csvParser) headerFor(sourceID string) ([]string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	header, ok := p.headers[sourceID]
	return header, ok
}

func (p *csvParser) parseHeader(sourceID, maprLine string) {
	var header []string
	start := 0
	delimiter := protocol.CSVDelimiter[0]
	for {
		field, next, done := scanDelimitedField(maprLine, start, delimiter)
		header = append(header, field)
		if done {
			break
		}
		start = next
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.headers[sourceID]; !exists {
		p.headers[sourceID] = header
	}
}
