package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mimecast/dtail/internal/cli"
	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/signal"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/profiling"
	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/user"
	"github.com/mimecast/dtail/internal/version"
)

// The evil begins here.
func main() {
	var args config.Args
	var checkHealth bool
	var displayColorTable bool
	var displayWideColorTable bool
	var displayVersion bool
	var grep string
	var legacyAuthKeyPath string
	var pprof string
	var shutdownAfter int
	var profileFlags profiling.Flags

	userName := user.Name()

	flag.BoolVar(&args.NoColor, "noColor", false, "Disable ANSII terminal colors")
	flag.BoolVar(&args.NoAuthKey, "no-auth-key", false, "Disable auth-key fast reconnect feature")
	flag.BoolVar(&args.LogPayload, "log-payload", false, "Also tee retrieved payload into the client log file (default: file keeps diagnostics only)")
	flag.BoolVar(&args.Quiet, "quiet", false, "Quiet output mode")
	flag.BoolVar(&args.RegexInvert, "invert", false, "Invert regex")
	flag.BoolVar(&args.InteractiveQuery, "interactive-query", false, "Enable interactive in-flight query control over supported sessions")
	flag.BoolVar(&args.Plain, "plain", false, "Plain output mode")
	flag.BoolVar(&args.TrustAllHosts, "trustAllHosts", false, "Trust all unknown host keys")
	flag.BoolVar(&checkHealth, "checkHealth", false, "Deprecated, flag will be removed soon")
	flag.BoolVar(&displayColorTable, "colorTable", false, "Show color table")
	flag.BoolVar(&displayWideColorTable, "wideColorTable", false, "Show a large color table")
	flag.BoolVar(&displayVersion, "version", false, "Display version")
	flag.IntVar(&args.ConnectionsPerCPU, "cpc", config.DefaultConnectionsPerCPU,
		"How many connections established per CPU core concurrently")
	flag.IntVar(&args.LContext.AfterContext, "after", 0, "Print lines of trailing context after matching lines")
	flag.IntVar(&args.LContext.BeforeContext, "before", 0, "Print lines of leading context before matching lines")
	flag.IntVar(&args.LContext.MaxCount, "max", 0, "Stop reading file after NUM matching lines")
	flag.IntVar(&args.SSHAgentKeyIndex, "agentKeyIndex", -1, "SSH agent key index to use (-1 for all keys)")
	flag.IntVar(&args.SSHPort, "port", config.DefaultSSHPort, "SSH server port")
	flag.IntVar(&args.Timeout, "timeout", 0, "Max time dtail server will collect data until disconnection")
	flag.IntVar(&shutdownAfter, "shutdownAfter", 3600*24, "Shutdown after so many seconds")
	flag.StringVar(&args.ConfigFile, "cfg", "", "Config file path")
	flag.StringVar(&args.ControlTTYPath, "control-tty", "/dev/tty", "TTY device for interactive query control")
	flag.StringVar(&args.Discovery, "discovery", "", "Server discovery method")
	flag.StringVar(&args.LogDir, "logDir", "~/log", "Log dir")
	flag.StringVar(&args.Logger, "logger", config.DefaultClientLogger, "Logger name")
	flag.StringVar(&args.LogLevel, "logLevel", config.DefaultLogLevel, "Log level")
	cli.BindAuthKeyFlags(flag.CommandLine, &legacyAuthKeyPath, &args)
	flag.StringVar(&args.QueryStr, "query", "", "Map reduce query")
	flag.StringVar(&args.RegexStr, "regex", ".", "Regular expression")
	flag.StringVar(&args.ServersStr, "servers", "", "Remote servers to connect")
	flag.StringVar(&args.UserName, "user", userName, "Your system user name")
	flag.StringVar(&args.What, "files", "", "File(s) to read")
	flag.StringVar(&grep, "grep", "", "Alias for -regex")
	flag.StringVar(&pprof, "pprof", "", "Start PProf server this address")

	// Add profiling flags
	profiling.AddFlags(&profileFlags)

	flag.Parse()
	if warning := cli.ApplyAuthKeyPathCompatibility(&args, legacyAuthKeyPath, cli.FlagWasSet("auth-key-path")); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	if grep != "" {
		args.RegexStr = grep
	}
	config.Setup(source.Client, &args, flag.Args())
	if displayVersion {
		runtimeCfg := config.CurrentRuntime()
		version.PrintAndExit(runtimeCfg.Client != nil && runtimeCfg.Client.TermColorsEnable)
	}
	if !args.Plain {
		if displayWideColorTable {
			color.TablePrintAndExit(true)
		}
		if displayColorTable {
			color.TablePrintAndExit(false)
		}
	}

	baseCtx, timeoutCancel := applyClientDeadlines(context.Background(), shutdownAfter, args.Timeout)

	runtime := cli.NewClientRuntime(baseCtx, profileFlags, "dtail")
	exitWithError := func(err error) {
		runtime.Stop()
		timeoutCancel()
		fmt.Fprintf(os.Stderr, "unable to initialize dtail client: %v\n", err)
		os.Exit(1)
	}

	if checkHealth {
		fmt.Println("WARN: DTail health check has moved to separate binary dtailhealth" +
			" - please adjust the monitoring scripts!")
		runtime.Stop()
		timeoutCancel()
		os.Exit(1)
	}

	runtime.StartPProf(pprof)
	runtime.LogStartupMetrics()

	var client clients.Client
	var err error
	args.Mode = omode.TailClient

	switch args.QueryStr {
	case "":
		if client, err = clients.NewTailClient(args); err != nil {
			exitWithError(err)
		}
	default:
		if client, err = clients.NewMaprClient(args, clients.DefaultMode); err != nil {
			exitWithError(err)
		}
	}

	status := client.Start(
		runtime.Context(),
		signal.InterruptChWithCancel(runtime.Context(), runtime.Cancel),
	)
	runtime.LogShutdownMetrics()
	runtime.Stop()
	timeoutCancel()
	os.Exit(status)
}

// applyClientDeadlines wraps ctx with the earliest of two absolute deadlines:
// the --shutdownAfter safety cap and the user's --timeout data-collection
// window. Whichever elapses first cancels the returned context, which propagates
// through the follow client's reconnect and per-connection read loops (both
// select on ctx.Done()), so client.Start returns cleanly and the process exits
// instead of auto-reconnecting for another cycle.
//
// Making --timeout a client-side deadline (rather than relying solely on the
// server closing the read) is what fixes the historical hang: the server-side
// read deadline fires at N seconds, but in tail+query mode the session stays
// alive via the map/aggregate command, so the client used to treat the closed
// read as a transient drop and reconnect indefinitely. A client-side deadline
// makes --timeout behave consistently for follows with or without --query, which
// matches the flag's help text ("Max time ... until disconnection").
//
// The two deadlines compose naturally (context deadlines nest, so the earlier
// one wins), so they never conflict with each other. A timeout of 0 (unset)
// contributes no deadline, preserving the previous behaviour. OS signals reach
// the same context via signal.InterruptChWithCancel in main.
func applyClientDeadlines(ctx context.Context, shutdownAfter, timeout int) (
	context.Context, context.CancelFunc) {

	var cancels []context.CancelFunc
	addDeadline := func(seconds int) {
		if seconds <= 0 {
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
		cancels = append(cancels, cancel)
	}

	addDeadline(shutdownAfter)
	addDeadline(timeout)

	return ctx, func() {
		// Release timers in reverse (inner first) to avoid leaking the parent
		// timer while an inner context still references it.
		for i := len(cancels) - 1; i >= 0; i-- {
			cancels[i]()
		}
	}
}
