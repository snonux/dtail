// Package main provides the DGrep (Distributed Grep) command-line tool.
// DGrep is a distributed version of the Unix grep command that can search
// for patterns in files across multiple remote servers simultaneously via SSH.
//
// Key features:
// - Distributed pattern matching across multiple servers
// - Regular expression support with invert option
// - Context lines (before/after matching lines)
// - Maximum match count limiting
// - SSH-based secure connections
// - Color-coded output with pattern highlighting
// - CPU and memory profiling support
// - Configurable connection pooling
//
// DGrep is particularly useful for searching log patterns across a fleet
// of servers, making it easy to correlate events or troubleshoot issues
// distributed across multiple systems.
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

// main is the entry point for the DGrep application.
// It parses command-line arguments, optionally starts CPU/memory profiling,
// initializes logging, creates a GrepClient, and searches for patterns across
// the specified servers. The function handles graceful shutdown and
// waits for all operations to complete.
func main() {
	var args config.Args
	var displayVersion bool
	var grep string
	var pprofAddr string
	var cpuprofile string
	var memprofile string
	userName := user.Name()

	flag.BoolVar(&args.NoColor, "noColor", false, "Disable ANSII terminal colors")
	flag.BoolVar(&args.Quiet, "quiet", false, "Quiet output mode")
	flag.BoolVar(&args.RegexInvert, "invert", false, "Invert regex")
	flag.BoolVar(&args.Plain, "plain", false, "Plain output mode")
	flag.BoolVar(&args.TrustAllHosts, "trustAllHosts", false, "Trust all unknown host keys")
	flag.BoolVar(&displayVersion, "version", false, "Display version")
	flag.IntVar(&args.ConnectionsPerCPU, "cpc", config.DefaultConnectionsPerCPU,
		"How many connections established per CPU core concurrently")
	flag.IntVar(&args.LContext.AfterContext, "after", 0, "Print lines of trailing context after matching lines")
	flag.IntVar(&args.LContext.BeforeContext, "before", 0, "Print lines of leading context before matching lines")
	flag.IntVar(&args.LContext.MaxCount, "max", 0, "Stop reading file after NUM matching lines")
	flag.IntVar(&args.SSHPort, "port", config.DefaultSSHPort, "SSH server port")
	flag.StringVar(&args.ConfigFile, "cfg", "", "Config file path")
	flag.StringVar(&args.Discovery, "discovery", "", "Server discovery method")
	flag.StringVar(&args.LogDir, "logDir", "~/log", "Log dir")
	flag.StringVar(&args.Logger, "logger", config.DefaultClientLogger, "Logger name")
	flag.StringVar(&args.LogLevel, "logLevel", config.DefaultLogLevel, "Log level")
	flag.StringVar(&args.SSHPrivateKeyFilePath, "key", "", "Path to private key")
	flag.StringVar(&args.RegexStr, "regex", ".", "Regular expression")
	flag.StringVar(&args.ServersStr, "servers", "", "Remote servers to connect")
	flag.StringVar(&args.UserName, "user", userName, "Your system user name")
	flag.StringVar(&args.What, "files", "", "File(s) to read")
	flag.StringVar(&grep, "grep", "", "Alias for -regex")
	flag.StringVar(&pprofAddr, "pprof", "", "Start PProf server this address")
	flag.StringVar(&cpuprofile, "cpuprofile", "", "Write CPU profile to file")
	flag.StringVar(&memprofile, "memprofile", "", "Write memory profile to file")

	flag.Parse()
	config.Setup(source.Client, &args, flag.Args())

	if displayVersion {
		version.PrintAndExit()
	}

	// CPU profiling
	if cpuprofile != "" {
		f, err := os.Create(cpuprofile)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	dlog.Start(ctx, &wg, source.Client)

	if grep != "" {
		args.RegexStr = grep
	}

	if pprofAddr != "" {
		go http.ListenAndServe(pprofAddr, nil)
		dlog.Client.Info("Started PProf", pprofAddr)
	}

	client, err := clients.NewGrepClient(args)
	if err != nil {
		panic(err)
	}

	status := client.Start(ctx, signal.InterruptCh(ctx))
	cancel()

	// Stop CPU profiling before exit
	if cpuprofile != "" {
		pprof.StopCPUProfile()
	}

	// Memory profiling  
	if memprofile != "" {
		f, err := os.Create(memprofile)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		pprof.WriteHeapProfile(f)
	}

	wg.Wait()
	os.Exit(status)
}
