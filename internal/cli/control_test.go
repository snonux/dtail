package cli

import (
	"flag"
	"io"
	"testing"

	"github.com/mimecast/dtail/internal/config"
)

// TestNormalizeControl covers the --control flags: json makes the session
// interactive and keeps diagnostics out of stdout unless --logger names a
// logger, an unknown channel and --control-in/--control-out without
// --control are rejected.
func TestNormalizeControl(t *testing.T) {
	tests := []struct {
		name        string
		argv        []string
		wantErr     bool
		interactive bool
		logger      string
	}{
		{name: "none", argv: nil},
		{name: "json", argv: []string{"--control", "json"}, interactive: true, logger: config.ControlLogger},
		{name: "json with a logger", argv: []string{"--control", "json", "--logger", "stdout"},
			interactive: true, logger: "stdout"},
		{name: "unknown channel", argv: []string{"--control", "xml"}, wantErr: true},
		{name: "input without a channel", argv: []string{"--control-in", "fifo"}, wantErr: true},
		{name: "output without a channel", argv: []string{"--control-out", "out"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var args config.Args
			fs := flag.NewFlagSet("dmap", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			BindCommonClientFlags(fs, &args)
			if err := fs.Parse(test.argv); err != nil {
				t.Fatal(err)
			}
			err := normalizeControl(&args)
			if (err != nil) != test.wantErr {
				t.Fatalf("normalizeControl() error = %v, want an error: %v", err, test.wantErr)
			}
			if args.InteractiveQuery != test.interactive || args.Logger != test.logger {
				t.Fatalf("interactive=%v logger=%q, want %v and %q", args.InteractiveQuery, args.Logger,
					test.interactive, test.logger)
			}
		})
	}
}

// TestControlDecidesInteractiveDefaults checks that a control channel makes
// dtail interactive without a terminal and whatever --no-interactive says,
// and that dmap holds its sessions (the builder's client) exactly when it
// has no query.
func TestControlDecidesInteractiveDefaults(t *testing.T) {
	for _, query := range []string{"", "select count($line)"} {
		args := config.Args{Control: config.ControlJSON, QueryStr: query}
		d := &InteractiveDefault{disabled: true, onTerminal: func() bool { return false },
			controlTTYUsable: func(string) bool { return false }}
		fs := flag.NewFlagSet("dmap", flag.ContinueOnError)
		if held := d.ResolveQueryBuilder(fs, &args); held != (query == "") || !args.InteractiveQuery {
			t.Fatalf("query %q: held=%v interactive=%v", query, held, args.InteractiveQuery)
		}
		args.InteractiveQuery = false
		if d.Resolve(fs, &args); !args.InteractiveQuery {
			t.Fatalf("query %q: dtail with a control channel is not interactive", query)
		}
	}
}
