package logformat

import "github.com/mimecast/dtail/internal/protocol"

type genericKVParser struct {
	defaultParser
}

var _ Parser = (*genericKVParser)(nil)

func newGenericKVParser(hostname, timeZoneName string, timeZoneOffset int) (*genericKVParser, error) {
	defaultParser, err := newDefaultParser(hostname, timeZoneName, timeZoneOffset)
	if err != nil {
		return &genericKVParser{}, err
	}
	return &genericKVParser{defaultParser: *defaultParser}, nil
}

func (p *genericKVParser) MakeFields(maprLine, _ string) (map[string]string, error) {
	fields := make(map[string]string, p.fieldsCapacity)
	p.addDefaultFields(fields, maprLine)
	start := 0
	delimiter := protocol.FieldDelimiter[0]

	for {
		token, next, done := scanDelimitedField(maprLine, start, delimiter)
		if err := p.addKeyValueField(fields, token); err != nil {
			continue
		}
		if done {
			break
		}
		start = next
	}

	return fields, nil
}
