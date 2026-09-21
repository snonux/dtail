package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/source"
)

func parseTestConfig(t *testing.T, body string) (initializer, error) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "dtail.json")
	writeTestConfig(t, configPath, body)
	in := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}
	return in, in.parseConfig(&Args{ConfigFile: configPath})
}

// TestParseConfigConvertsColorNames checks that configured colour names become
// terminal escape codes and that keys the file omits keep their defaults.
func TestParseConfigConvertsColorNames(t *testing.T) {
	in, err := parseTestConfig(t, `{"Client": {"TermColors": {
  "Remote": {"TextFg": "Red", "TextBg": "yellow", "TextAttr": "BOLD"},
  "Common": {"SeverityWarnFg": "Green", "SeverityWarnAttr": ""},
  "MaprTable": {"HeaderSortKeyAttr": "Italic", "RawQueryBg": "Default"}
}}}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	want := DefaultTermColors()
	want.Remote.TextFg = color.FgRed
	want.Remote.TextBg = color.BgYellow
	want.Remote.TextAttr = color.AttrBold
	want.Common.SeverityWarnFg = color.FgGreen
	want.Common.SeverityWarnAttr = color.AttrNone
	want.MaprTable.HeaderSortKeyAttr = color.AttrItalic
	want.MaprTable.RawQueryBg = color.BgDefault
	if got := in.Client.TermColors; got != want {
		t.Fatalf("TermColors = %+v, want %+v", got, want)
	}
}

// TestParseConfigKeepsLegacyEscapeColors checks that raw escape sequences from
// older config files are still used unchanged.
func TestParseConfigKeepsLegacyEscapeColors(t *testing.T) {
	in, err := parseTestConfig(t, `{"Client": {"TermColors": {
  "Server": {"TextFg": "\u001b[31m", "TextBg": "\u001b[48;5;17m", "TextAttr": "\u001b[4m"}
}}}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	got := in.Client.TermColors.Server
	if got.TextFg != color.FgRed || got.TextBg != color.BgColor("\x1b[48;5;17m") ||
		got.TextAttr != color.AttrUnderline {
		t.Fatalf("legacy escape colours = %q %q %q", got.TextFg, got.TextBg, got.TextAttr)
	}
}

func TestSetupRejectsInvalidColorName(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "foreground", body: `{"Client": {"TermColors": {"Client": {"TextFg": "Purple"}}}}`,
			wantErr: `invalid foreground color "Purple"`},
		{name: "background", body: `{"Client": {"TermColors": {"Remote": {"IDBg": "Bleu"}}}}`,
			wantErr: `invalid background color "Bleu"`},
		{name: "attribute", body: `{"Client": {"TermColors": {"MaprTable": {"HeaderAttr": "Strike"}}}}`,
			wantErr: `invalid text attribute "Strike"`},
		{name: "empty colour", body: `{"Client": {"TermColors": {"Common": {"SeverityErrorFg": ""}}}}`,
			wantErr: `invalid foreground color ""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "dtail.json")
			writeTestConfig(t, configPath, tt.body)
			_, err := SetupRuntime(source.Client, &Args{ConfigFile: configPath}, nil)
			if err == nil {
				t.Fatal("SetupRuntime accepted an invalid colour name")
			}
			for _, want := range []string{"parse config file", configPath, tt.wantErr, "case-insensitive"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// TestShippedConfigsDecodeToDefaultPalette checks that the palettes shipped in
// the example and docker configs decode to escape codes; both spell out the
// built-in default palette (or omit it), so they must equal the defaults.
func TestShippedConfigsDecodeToDefaultPalette(t *testing.T) {
	for _, path := range []string{"../../examples/dtail.json.example", "../../docker/dtail.json"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			in := initializer{
				Common: newDefaultCommonConfig(),
				Server: newDefaultServerConfig(),
				Client: newDefaultClientConfig(),
			}
			if err := in.parseConfig(&Args{ConfigFile: path}); err != nil {
				t.Fatalf("parseConfig: %v", err)
			}
			if got, want := in.Client.TermColors, DefaultTermColors(); got != want {
				t.Fatalf("TermColors = %+v, want defaults %+v", got, want)
			}
		})
	}
}
