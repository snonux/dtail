package logformat

import "github.com/mimecast/dtail/internal/protocol"

type genericKVParser struct {
	defaultParser
}

var _ Parser = (*genericKVParser)(nil)
var _ FieldsIntoParser = (*genericKVParser)(nil)

func newGenericKVParser(hostname, timeZoneName string, timeZoneOffset int) (*genericKVParser, error) {
	defaultParser, err := newDefaultParser(hostname, timeZoneName, timeZoneOffset)
	if err != nil {
		return &genericKVParser{}, err
	}
	return &genericKVParser{defaultParser: *defaultParser}, nil
}

func (p *genericKVParser) MakeFields(maprLine, sourceID string) (map[string]string, error) {
	fields := make(map[string]string, p.fieldsCapacity)
	return fields, p.MakeFieldsInto(fields, maprLine, sourceID)
}

// MakeFieldsInto must be defined here rather than inherited from the embedded
// defaultParser: the promoted method would parse the line in DTail's own
// MAPREDUCE layout instead of the generic key-value one.
func (p *genericKVParser) MakeFieldsInto(dst map[string]string, maprLine, _ string) error {
	clear(dst)
	p.addDefaultFields(dst, maprLine)
	start := 0

	for {
		token, next, done := protocol.ScanField(maprLine, start)
		// Generic key-value logs may mix structured and unstructured fields.
		// Ignore malformed fields while continuing to parse later tokens.
		_ = p.addKeyValueField(dst, token)
		if done {
			break
		}
		start = next
	}

	return nil
}
