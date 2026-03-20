package logformat

import (
	"fmt"

	"github.com/mimecast/dtail/internal/protocol"
)

type csvParser struct {
	defaultParser
	header    []string
	hasHeader bool
}

var _ Parser = (*csvParser)(nil)

func newCSVParser(hostname, timeZoneName string, timeZoneOffset int) (*csvParser, error) {
	defaultParser, err := newDefaultParser(hostname, timeZoneName, timeZoneOffset)
	if err != nil {
		return &csvParser{}, err
	}
	return &csvParser{defaultParser: *defaultParser}, nil
}

func (p *csvParser) MakeFields(maprLine string) (map[string]string, error) {
	if !p.hasHeader {
		p.parseHeader(maprLine)
		return nil, ErrIgnoreFields
	}

	fields := make(map[string]string, p.fieldsCapacity)
	p.addDefaultFields(fields, maprLine)
	start := 0
	column := 0
	delimiter := protocol.CSVDelimiter[0]

	for {
		value, next, done := scanDelimitedField(maprLine, start, delimiter)
		if column >= len(p.header) {
			return fields, fmt.Errorf("CSV file seems corrupted, more fields than header values?")
		}
		p.addDynamicField(fields, p.header[column], value)
		column++
		if done {
			break
		}
		start = next
	}

	return fields, nil
}

func (p *csvParser) parseHeader(maprLine string) {
	start := 0
	delimiter := protocol.CSVDelimiter[0]
	for {
		header, next, done := scanDelimitedField(maprLine, start, delimiter)
		p.header = append(p.header, header)
		if done {
			break
		}
		start = next
	}
	p.hasHeader = true
}
