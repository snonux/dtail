package color

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// legacySGR matches one raw ANSI SGR escape sequence such as "\x1b[37m". Old
// config files stored colours this way because the decoder used to copy the
// configured string verbatim into the output.
var legacySGR = regexp.MustCompile(`^\x1b\[[0-9;]*m$`)

// UnmarshalJSON decodes a foreground colour from a config file. It accepts a
// colour name from ColorNames (case-insensitive) or, for backward
// compatibility, a raw ANSI SGR escape sequence, which is kept unchanged.
func (c *FgColor) UnmarshalJSON(data []byte) error {
	sp, err := decodeConfigString(data, "foreground color")
	if err != nil || sp == nil {
		return err
	}
	s := *sp
	if legacySGR.MatchString(s) {
		*c = FgColor(s)
		return nil
	}
	fg, err := ToFgColor(s)
	if err != nil {
		return invalidNameError("foreground color", s, ColorNames)
	}
	*c = fg
	return nil
}

// UnmarshalJSON decodes a background colour from a config file. It accepts a
// colour name from ColorNames (case-insensitive) or, for backward
// compatibility, a raw ANSI SGR escape sequence, which is kept unchanged.
func (c *BgColor) UnmarshalJSON(data []byte) error {
	sp, err := decodeConfigString(data, "background color")
	if err != nil || sp == nil {
		return err
	}
	s := *sp
	if legacySGR.MatchString(s) {
		*c = BgColor(s)
		return nil
	}
	bg, err := ToBgColor(s)
	if err != nil {
		return invalidNameError("background color", s, ColorNames)
	}
	*c = bg
	return nil
}

// UnmarshalJSON decodes a text attribute from a config file. It accepts an
// attribute name from AttributeNames (case-insensitive), the empty string for
// no attribute or, for backward compatibility, a raw ANSI SGR escape sequence,
// which is kept unchanged.
func (a *Attribute) UnmarshalJSON(data []byte) error {
	sp, err := decodeConfigString(data, "text attribute")
	if err != nil || sp == nil {
		return err
	}
	s := *sp
	if legacySGR.MatchString(s) {
		*a = Attribute(s)
		return nil
	}
	attr, err := ToAttribute(s)
	if err != nil {
		return invalidNameError("text attribute", s, AttributeNames)
	}
	*a = attr
	return nil
}

// decodeConfigString decodes a JSON string. It returns nil for a JSON null so
// the caller keeps its current (default) value, like encoding/json does for
// plain string fields.
func decodeConfigString(data []byte, kind string) (*string, error) {
	var s *string
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid %s %s: must be a string: %w", kind, data, err)
	}
	return s, nil
}

func invalidNameError(kind, value string, names []string) error {
	return fmt.Errorf("invalid %s %q: must be one of %s (case-insensitive)",
		kind, value, strings.Join(names, ", "))
}
