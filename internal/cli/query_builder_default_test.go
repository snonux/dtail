package cli

import (
	"flag"
	"io"
	"testing"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

// parseMapFlags binds the common client flags, --query and --no-interactive
// on a fresh flag set as dmap does, parses argv and resolves the query
// builder with the given terminal and control TTY probes.
func parseMapFlags(t *testing.T, argv []string, onTerminal, ttyUsable bool) (config.Args, bool) {
	t.Helper()
	args := config.Args{Mode: omode.MapClient}
	fs := flag.NewFlagSet("dmap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	BindCommonClientFlags(fs, &args)
	fs.StringVar(&args.QueryStr, "query", "", "Map reduce query")
	d := BindQueryBuilderDefault(fs)
	d.onTerminal = func() bool { return onTerminal }
	d.controlTTYUsable = func(string) bool { return ttyUsable }
	if err := fs.Parse(argv); err != nil {
		t.Fatalf("parse %q: %v", argv, err)
	}
	builder := d.ResolveQueryBuilder(fs, &args)
	return args, builder
}

// TestResolveQueryBuilder covers dmap's start-up decision: without --query
// the query builder opens on a terminal with a usable control TTY or with an
// explicit --interactive-query (which alone keeps the session open after the
// query ran); with --query, --no-interactive, --interactive-query=false or
// without a terminal nothing changes, so a missing query stays an error.
func TestResolveQueryBuilder(t *testing.T) {
	const query = "from STATS select count($line)"
	tests := []struct {
		name            string
		argv            []string
		onTerminal      bool
		ttyUsable       bool
		wantBuilder     bool
		wantInteractive bool
	}{
		{name: "no query on a terminal", onTerminal: true, ttyUsable: true, wantBuilder: true},
		{name: "blank query on a terminal", argv: []string{"--query", "  "},
			onTerminal: true, ttyUsable: true, wantBuilder: true},
		{name: "no query, no terminal (pipe, cron)", ttyUsable: true},
		{name: "no query, terminal without control TTY", onTerminal: true},
		{name: "no query, --no-interactive", argv: []string{"--no-interactive"},
			onTerminal: true, ttyUsable: true},
		{name: "no query, --interactive-query=false", argv: []string{"--interactive-query=false"},
			onTerminal: true, ttyUsable: true},
		{name: "no query, explicit --interactive-query", argv: []string{"--interactive-query"},
			wantBuilder: true, wantInteractive: true},
		{name: "no query, --no-interactive wins", argv: []string{"--interactive-query", "--no-interactive"},
			onTerminal: true, ttyUsable: true},
		{name: "query on a terminal", argv: []string{"--query", query}, onTerminal: true, ttyUsable: true},
		{name: "query with --interactive-query", argv: []string{"--query", query, "--interactive-query"},
			onTerminal: true, ttyUsable: true, wantInteractive: true},
		{name: "query with --no-interactive",
			argv:       []string{"--query", query, "--interactive-query", "--no-interactive"},
			onTerminal: true, ttyUsable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, builder := parseMapFlags(t, tt.argv, tt.onTerminal, tt.ttyUsable)
			if builder != tt.wantBuilder {
				t.Errorf("query builder = %v, want %v", builder, tt.wantBuilder)
			}
			if args.InteractiveQuery != tt.wantInteractive {
				t.Errorf("InteractiveQuery = %v, want %v", args.InteractiveQuery, tt.wantInteractive)
			}
		})
	}
}

// TestResolveQueryBuilderProbesTTYOnlyWhenUndecided checks that the control
// TTY is only opened for the one case it decides: no query and nothing
// given explicitly.
func TestResolveQueryBuilderProbesTTYOnlyWhenUndecided(t *testing.T) {
	for _, argv := range [][]string{{"--query", "from STATS select count($line)"},
		{"--interactive-query"}, {"--no-interactive"}} {
		args := config.Args{Mode: omode.MapClient}
		fs := flag.NewFlagSet("dmap", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		BindCommonClientFlags(fs, &args)
		fs.StringVar(&args.QueryStr, "query", "", "Map reduce query")
		d := BindQueryBuilderDefault(fs)
		d.onTerminal = func() bool { return true }
		d.controlTTYUsable = func(string) bool {
			t.Errorf("%q: the control TTY was probed", argv)
			return true
		}
		if err := fs.Parse(argv); err != nil {
			t.Fatal(err)
		}
		d.ResolveQueryBuilder(fs, &args)
	}
}
