package cli

import (
	"flag"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/mimecast/dtail/internal/config"
)

// interactiveQueryFlag is the common client flag that enables interactive
// query control (BindCommonClientFlags).
const interactiveQueryFlag = "interactive-query"

// InteractiveDefault makes interactive query control the default of a client
// (dtail) when it runs in a terminal, and binds --no-interactive to turn it
// off. For dmap it decides the same way whether the query builder opens for
// a missing query (BindQueryBuilderDefault, ResolveQueryBuilder). It lives
// here rather than in cmd/dtail because the build tooling
// compiles each command from its single main.go file.
type InteractiveDefault struct {
	// disabled is --no-interactive. It wins over --interactive-query.
	disabled bool
	// onTerminal reports whether the client runs in an interactive terminal
	// session. Tests replace it.
	onTerminal func() bool
	// controlTTYUsable reports whether the control TTY can be opened. Tests
	// replace it.
	controlTTYUsable func(path string) bool
}

// BindInteractiveDefault registers --no-interactive on fs.
func BindInteractiveDefault(fs *flag.FlagSet) *InteractiveDefault {
	d := &InteractiveDefault{onTerminal: stdioIsTerminal, controlTTYUsable: canOpenControlTTY}
	fs.BoolVar(&d.disabled, "no-interactive", false,
		"Disable interactive query control (on by default when stdin and stdout are a terminal)")
	return d
}

// Resolve sets args.InteractiveQuery after fs was parsed. Unlike the one-shot
// clients (dcat, dgrep, dmap), whose session would no longer end by itself, a
// dtail follow never ends on its own, so interactive query control costs
// nothing there and is its default. The rules:
//
//   - --no-interactive always disables it.
//   - An explicit --interactive-query (true or false) is kept as given.
//   - Otherwise it is enabled only when stdin and stdout are both a terminal
//     and the control TTY can be opened. Pipes, redirections (also from
//     /dev/null, which is a character device but no terminal), cron, CI and
//     ssh without a TTY keep the non-interactive behaviour: the control loop
//     would otherwise fail to open the TTY and end the client with status 1.
func (d *InteractiveDefault) Resolve(fs *flag.FlagSet, args *config.Args) {
	if args.Control != "" {
		// A control channel is an interactive session by itself, with or
		// without a terminal (normalizeControl), and nothing turns it off.
		args.InteractiveQuery = true
		return
	}
	if d.disabled {
		args.InteractiveQuery = false
		return
	}
	if flagWasSet(fs, interactiveQueryFlag) {
		return
	}
	args.InteractiveQuery = d.onTerminal() && d.controlTTYUsable(args.ControlTTYPath)
}

// BindQueryBuilderDefault registers --no-interactive on fs for dmap, whose
// terminal default is not interactive query control but the query builder
// for a missing --query (ResolveQueryBuilder).
func BindQueryBuilderDefault(fs *flag.FlagSet) *InteractiveDefault {
	d := &InteractiveDefault{onTerminal: stdioIsTerminal, controlTTYUsable: canOpenControlTTY}
	fs.BoolVar(&d.disabled, "no-interactive", false,
		"Never open the query builder or interactive query control "+
			"(without --query the builder opens when stdin and stdout are a terminal)")
	return d
}

// ResolveQueryBuilder reports whether dmap opens its query builder
// (clients.NewQueryBuilderClient) instead of failing for the missing query.
// It is called after fs was parsed and after the client setup
// (config.SetupRuntime), which takes a positional argument for the query, so
// that args.QueryStr is the query however it was given. It leaves
// args.InteractiveQuery as given, except that --no-interactive clears it:
// for the builder it then says whether the session stays open after the
// built query ran (an explicit --interactive-query) or dmap runs it and
// exits (the default). The rules:
//
//   - --no-interactive never opens it (and disables --interactive-query).
//   - With a --query that is not blank dmap runs as before.
//   - Without one, an explicit --interactive-query opens it and
//     --interactive-query=false does not.
//   - Otherwise it opens only when stdin and stdout are both a terminal and
//     the control TTY can be opened (as dtail's interactive default,
//     Resolve), so that a pipe, a script, cron and CI keep the error for the
//     missing query.
func (d *InteractiveDefault) ResolveQueryBuilder(fs *flag.FlagSet, args *config.Args) bool {
	if args.Control != "" {
		// With a control channel and no query, dmap holds its sessions like
		// the builder and runs the queries it is sent; with a query it is
		// interactive dmap, controlled over the channel.
		args.InteractiveQuery = true
		return strings.TrimSpace(args.QueryStr) == ""
	}
	if d.disabled {
		args.InteractiveQuery = false
		return false
	}
	if strings.TrimSpace(args.QueryStr) != "" {
		return false
	}
	if flagWasSet(fs, interactiveQueryFlag) {
		return args.InteractiveQuery
	}
	return d.onTerminal() && d.controlTTYUsable(args.ControlTTYPath)
}

// flagWasSet reports whether name was given on the command line.
func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// stdioIsTerminal reports whether both stdin and stdout are terminals.
// stdin matters because the host key prompt and the control loop read the
// terminal; stdout because interactive control is for a person watching the
// output, not for a pipe into another program.
func stdioIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// canOpenControlTTY reports whether the control TTY can be opened read-write,
// as the interactive control loop opens it. A process without a controlling
// terminal fails to open /dev/tty although its stdio may be terminals.
func canOpenControlTTY(path string) bool {
	tty, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = tty.Close()
	return true
}
