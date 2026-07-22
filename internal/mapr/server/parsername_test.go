package server

import (
	"testing"

	"github.com/mimecast/dtail/internal/mapr"
)

// TestResolveParserName locks the log-format selection rules that decide which
// parser (and therefore which fields) a mapr query gets. These rules are the
// root cause of a common surprise: a query without a "from TABLE" clause is
// downgraded to the "generic" parser, which exposes none of the dynamic
// key=value fields or the default-format "$"-variables. See
// doc/querylanguage.md ("Selecting the log format and dynamic fields").
func TestResolveParserName(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		configured string
		want       string
	}{
		{
			name:  "explicit logformat wins over everything",
			query: "from STATS select $line logformat generickv",
			want:  "generickv",
		},
		{
			name:  "explicit logformat wins even without a from clause",
			query: "select service logformat generickv",
			want:  "generickv",
		},
		{
			name:  "no from clause downgrades to generic",
			query: "select service,sum(bytes) group by service",
			want:  "generic",
		},
		{
			name:  "from TABLE without configured format uses default",
			query: "from STATS select lifetimeConnections",
			want:  "default",
		},
		{
			name:       "from TABLE honours the configured default format",
			query:      "from STATS select lifetimeConnections",
			configured: "mimecast",
			want:       "mimecast",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query, err := mapr.NewQuery(tc.query)
			if err != nil {
				t.Fatalf("NewQuery(%q) failed: %v", tc.query, err)
			}
			if got := resolveParserName(query, tc.configured); got != tc.want {
				t.Errorf("resolveParserName(%q, %q) = %q, want %q",
					tc.query, tc.configured, got, tc.want)
			}
		})
	}
}
