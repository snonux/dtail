package logformat

type genericParser struct {
	defaultParser
}

var _ Parser = (*genericParser)(nil)
var _ FieldsIntoParser = (*genericParser)(nil)

func newGenericParser(hostname, timeZoneName string, timeZoneOffset int) (*genericParser, error) {
	defaultParser, err := newDefaultParser(hostname, timeZoneName, timeZoneOffset)
	if err != nil {
		return &genericParser{}, err
	}
	return &genericParser{defaultParser: *defaultParser}, nil
}

func (p *genericParser) MakeFields(maprLine, sourceID string) (map[string]string, error) {
	fields := make(map[string]string, p.fieldsCapacity)
	return fields, p.MakeFieldsInto(fields, maprLine, sourceID)
}

// MakeFieldsInto must be defined here rather than inherited from the embedded
// defaultParser: the promoted method would parse the line in DTail's own
// MAPREDUCE layout instead of the generic one.
func (p *genericParser) MakeFieldsInto(dst map[string]string, maprLine, _ string) error {
	clear(dst)
	p.addDefaultFields(dst, maprLine)

	return nil
}
