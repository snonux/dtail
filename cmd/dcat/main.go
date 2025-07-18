package main

import (
	"context"
	"flag"
	"os"
	"sync"

	"net/http"
	_ "net/http"
	_ "net/http/pprof"

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
	var pprof string
	var profileFlags profiling.Flags

	userName := user.Name()

	flag.BoolVar(&args.NoColor, "noColor", false, "Disable ANSII terminal colors")
	flag.BoolVar(&args.Quiet, "quiet", false, "Quiet output mode")
	flag.BoolVar(&args.Plain, "plain", false, "Plain output mode")
	flag.BoolVar(&args.TrustAllHosts, "trustAllHosts", false, "Trust all unknown host keys")
	flag.BoolVar(&displayVersion, "version", false, "Display version")
	flag.IntVar(&args.ConnectionsPerCPU, "cpc", config.DefaultConnectionsPerCPU,
		"How many connections established per CPU core concurrently")
	flag.IntVar(&args.SSHPort, "port", config.DefaultSSHPort, "SSH server port")
	flag.StringVar(&args.ConfigFile, "cfg", "", "Config file path")
	flag.StringVar(&args.Discovery, "discovery", "", "Server discovery method")
	flag.StringVar(&args.LogDir, "logDir", "~/log", "Log dir")
	flag.StringVar(&args.Logger, "logger", config.DefaultClientLogger, "Logger name")
	flag.StringVar(&args.LogLevel, "logLevel", config.DefaultLogLevel, "Log level")
	flag.StringVar(&args.SSHPrivateKeyFilePath, "key", "", "Path to private key")
	flag.StringVar(&args.ServersStr, "servers", "", "Remote servers to connect")
	flag.StringVar(&args.UserName, "user", userName, "Your system user name")
	flag.StringVar(&args.What, "files", "", "File(s) to read")
	flag.StringVar(&pprof, "pprof", "", "Start PProf server this address")
	
	// Add profiling flags
	profiling.AddFlags(&profileFlags)

	flag.Parse()
	config.Setup(source.Client, &args, flag.Args())

	if displayVersion {
		version.PrintAndExit()
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	dlog.Start(ctx, &wg, source.Client)

	// Set up profiling
	profiler := profiling.NewProfiler(profileFlags.ToConfig("dcat"))
	defer profiler.Stop()

	if pprof != "" {
		dlog.Client.Info("Starting PProf", pprof)
		go func() {
			panic(http.ListenAndServe(pprof, nil))
		}()
	}

	// Log initial metrics if profiling is enabled
	if profileFlags.Enabled() {
		profiler.LogMetrics("startup")
	}

	client, err := clients.NewCatClient(args)
	if err != nil {
		panic(err)
	}

	status := client.Start(ctx, signal.InterruptCh(ctx))
	
	// Log final metrics if profiling is enabled
	if profileFlags.Enabled() {
		profiler.LogMetrics("shutdown")
	}
	
	// Stop profiler before exit
	profiler.Stop()
	
	cancel()

	wg.Wait()
	os.Exit(status)
}
