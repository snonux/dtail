package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/mimecast/dtail/internal/cli"
	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/signal"
	"github.com/mimecast/dtail/internal/profiling"
	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/user"
	"github.com/mimecast/dtail/internal/version"
)

// The evil begins here.
func main() {
	var args config.Args
	var displayVersion bool
	var grep string
	var legacyAuthKeyPath string
	var pprof string
	var profileFlags profiling.Flags
	flag.BoolVar(&args.NoColor, "noColor", false, "Disable ANSII terminal colors")
	flag.BoolVar(&args.NoAuthKey, "no-auth-key", false, "Disable auth-key fast reconnect feature")
	flag.BoolVar(&args.LogPayload, "log-payload", false, "Also tee retrieved payload into the client log file (default: file keeps diagnostics only)")
	flag.BoolVar(&args.Quiet, "quiet", false, "Quiet output mode")
	flag.BoolVar(&args.RegexInvert, "invert", false, "Invert regex")
	flag.BoolVar(&args.InteractiveQuery, "interactive-query", false, "Enable interactive in-flight query control over supported sessions")
	flag.BoolVar(&args.Plain, "plain", false, "Plain output mode")
	flag.BoolVar(&args.TrustAllHosts, "trustAllHosts", false, "Trust all unknown host keys")
	flag.BoolVar(&displayVersion, "version", false, "Display version")
	flag.IntVar(&args.ConnectionsPerCPU, "cpc", config.DefaultConnectionsPerCPU,
		"How many connections established per CPU core concurrently")
	flag.IntVar(&args.AfterContext, "after", 0, "Print lines of trailing context after matching lines")
	flag.IntVar(&args.BeforeContext, "before", 0, "Print lines of leading context before matching lines")
	flag.IntVar(&args.MaxCount, "max", 0, "Stop reading file after NUM matching lines")
	flag.IntVar(&args.SSHAgentKeyIndex, "agentKeyIndex", -1, "SSH agent key index to use (-1 for all keys)")
	flag.IntVar(&args.SSHPort, "port", config.DefaultSSHPort, "SSH server port")
	flag.StringVar(&args.ConfigFile, "cfg", "", "Config file path")
	flag.StringVar(&args.ControlTTYPath, "control-tty", "/dev/tty", "TTY device for interactive query control")
	flag.StringVar(&args.Discovery, "discovery", "", "Server discovery method")
	flag.StringVar(&args.LogDir, "logDir", "~/log", "Log dir")
	flag.StringVar(&args.Logger, "logger", config.DefaultClientLogger, "Logger name")
	flag.StringVar(&args.LogLevel, "logLevel", config.DefaultLogLevel, "Log level")
	cli.BindAuthKeyFlags(flag.CommandLine, &legacyAuthKeyPath, &args)
	flag.StringVar(&args.RegexStr, "regex", ".", "Regular expression")
	flag.StringVar(&args.ServersStr, "servers", "", "Remote servers to connect")
	flag.StringVar(&args.UserName, "user", "", "Your system user name")
	flag.StringVar(&args.What, "files", "", "File(s) to read")
	flag.StringVar(&grep, "grep", "", "Alias for -regex")
	flag.StringVar(&pprof, "pprof", "", "Start PProf server this address")

	// Add profiling flags
	profiling.AddFlags(&profileFlags)

	flag.Parse()
	if warning := cli.ApplyAuthKeyPathCompatibility(&args, legacyAuthKeyPath, cli.FlagWasSet("auth-key-path")); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	if err := config.Setup(source.Client, &args, flag.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "unable to configure dgrep: %v\n", err)
		os.Exit(1)
	}

	if displayVersion {
		runtimeCfg := config.CurrentRuntime()
		version.PrintAndExit(runtimeCfg.Client != nil && runtimeCfg.Client.TermColorsEnable)
	}
	if args.UserName == "" {
		userName, err := user.CurrentName()
		if err != nil {
			fmt.Fprintf(os.Stderr, "unable to determine dgrep user: %v\n", err)
			os.Exit(1)
		}
		args.UserName = userName
	}

	runtime, err := cli.NewClientRuntime(context.Background(), profileFlags, "dgrep")
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to initialize dgrep runtime: %v\n", err)
		os.Exit(1)
	}

	if grep != "" {
		args.RegexStr = grep
	}

	runtime.StartPProf(pprof)
	runtime.LogStartupMetrics()

	client, err := clients.NewGrepClient(args)
	if err != nil {
		dlog.Client.Error("Unable to create dgrep client", err)
		fmt.Fprintf(os.Stderr, "unable to create dgrep client: %v\n", err)
		runtime.Stop()
		os.Exit(1)
	}

	status := client.Start(runtime.Context(), signal.InterruptChWithCancel(runtime.Context(), runtime.Cancel))
	runtime.LogShutdownMetrics()
	runtime.Stop()
	os.Exit(status)
}
