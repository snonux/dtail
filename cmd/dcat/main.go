// Package main provides the DCat (Distributed Cat) command-line tool.
// DCat is a distributed version of the Unix cat command that can read and
// concatenate files across multiple remote servers simultaneously via SSH.
//
// Key features:
// - Distributed file reading across multiple servers
// - SSH-based secure connections
// - Configurable connection pooling
// - CPU and memory profiling support
// - Color-coded output (can be disabled)
// - Quiet and plain output modes
//
// DCat is particularly useful for quickly examining log files or configuration
// files across a fleet of servers without having to SSH to each one individually.
package main

import (
	"context"
	"flag"
	"os"
	"runtime/pprof"
	"sync"

	"net/http"
	_ "net/http"
	_ "net/http/pprof"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/signal"
	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/user"
	"github.com/mimecast/dtail/internal/version"
)

// main is the entry point for the DCat application.
// It parses command-line arguments, optionally starts CPU profiling,
// initializes logging, creates a CatClient, and processes files across
// the specified servers. The function handles graceful shutdown and
// waits for all operations to complete.
func main() {
	var args config.Args
	var displayVersion bool
	var pprofAddr string
	var cpuprofile string

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
	flag.StringVar(&pprofAddr, "pprof", "", "Start PProf server this address")
	flag.StringVar(&cpuprofile, "cpuprofile", "", "Write CPU profile to file")

	flag.Parse()
	config.Setup(source.Client, &args, flag.Args())

	if displayVersion {
		version.PrintAndExit()
	}

	if cpuprofile != "" {
		f, err := os.Create(cpuprofile)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			panic(err)
		}
		defer pprof.StopCPUProfile()
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	dlog.Start(ctx, &wg, source.Client)

	if pprofAddr != "" {
		go http.ListenAndServe(pprofAddr, nil)
		dlog.Client.Info("Started PProf", pprofAddr)
	}

	client, err := clients.NewCatClient(args)
	if err != nil {
		panic(err)
	}

	status := client.Start(ctx, signal.InterruptCh(ctx))
	cancel()

	wg.Wait()
	
	if cpuprofile != "" {
		pprof.StopCPUProfile()
	}
	
	os.Exit(status)
}
