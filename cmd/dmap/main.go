package main

import (
	"context"
	"flag"
	"os"

	"github.com/mimecast/dtail/internal/cli"
	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/signal"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/profiling"
	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/user"
	"github.com/mimecast/dtail/internal/version"
)

// The evil begins here.
func main() {
	var displayVersion bool
	var pprof string
	var profileFlags profiling.Flags

	args := config.Args{
		Mode:             omode.MapClient,
		SSHAgentKeyIndex: -1,
	}
	userName := user.Name()

	flag.BoolVar(&args.NoColor, "noColor", false, "Disable ANSII terminal colors")
	flag.BoolVar(&args.NoAuthKey, "no-auth-key", false, "Disable auth-key fast reconnect feature")
	flag.BoolVar(&args.Quiet, "quiet", false, "Quiet output mode")
	flag.BoolVar(&args.InteractiveQuery, "interactive-query", false, "Enable interactive in-flight query control over supported sessions")
	flag.BoolVar(&args.Plain, "plain", false, "Plain output mode")
	flag.BoolVar(&args.TrustAllHosts, "trustAllHosts", false, "Trust all unknown host keys")
	flag.BoolVar(&displayVersion, "version", false, "Display version")
	flag.IntVar(&args.ConnectionsPerCPU, "cpc", config.DefaultConnectionsPerCPU,
		"How many connections established per CPU core concurrently")
	flag.IntVar(&args.SSHAgentKeyIndex, "agentKeyIndex", -1, "SSH agent key index to use (-1 for all keys)")
	flag.IntVar(&args.SSHPort, "port", config.DefaultSSHPort, "SSH server port")
	flag.IntVar(&args.Timeout, "timeout", 0, "Max time dtail server will collect data until disconnection")
	flag.StringVar(&args.ConfigFile, "cfg", "", "Config file path")
	flag.StringVar(&args.ControlTTYPath, "control-tty", "/dev/tty", "TTY device for interactive query control")
	flag.StringVar(&args.Discovery, "discovery", "", "Server discovery method")
	flag.StringVar(&args.LogDir, "logDir", "~/log", "Log dir")
	flag.StringVar(&args.Logger, "logger", config.DefaultClientLogger, "Logger name")
	flag.StringVar(&args.LogLevel, "logLevel", config.DefaultLogLevel, "Log level")
	flag.StringVar(&args.SSHPrivateKeyFilePath, "key", "", "Path to private key")
	flag.StringVar(&args.SSHPrivateKeyFilePath, "auth-key-path", "", "Path to auth key/private key (default ~/.ssh/id_rsa)")
	flag.StringVar(&args.QueryStr, "query", "", "Map reduce query")
	flag.StringVar(&args.ServersStr, "servers", "", "Remote servers to connect")
	flag.StringVar(&args.UserName, "user", userName, "Your system user name")
	flag.StringVar(&args.What, "files", "", "File(s) to read")
	flag.StringVar(&pprof, "pprof", "", "Start PProf server this address")

	// Add profiling flags
	profiling.AddFlags(&profileFlags)

	flag.Parse()
	config.Setup(source.Client, &args, flag.Args())

	if displayVersion {
		runtimeCfg := config.CurrentRuntime()
		version.PrintAndExit(runtimeCfg.Client != nil && runtimeCfg.Client.TermColorsEnable)
	}

	runtime := cli.NewClientRuntime(context.Background(), profileFlags, "dmap")
	runtime.StartPProf(pprof)
	runtime.LogStartupMetrics()

	client, err := clients.NewMaprClient(args, clients.DefaultMode)
	if err != nil {
		runtime.Stop()
		dlog.Client.FatalPanic(err)
	}

	status := client.Start(
		runtime.Context(),
		signal.InterruptChWithCancel(runtime.Context(), runtime.Cancel),
	)
	runtime.LogShutdownMetrics()
	runtime.Stop()
	os.Exit(status)
}
