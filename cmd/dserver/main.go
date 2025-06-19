// Package main provides the DTail server daemon (dserver).
// The dserver is an SSH-based server that processes distributed log operations
// from DTail clients. It handles incoming SSH connections, authenticates users,
// and processes various commands like tail, cat, grep, and MapReduce operations.
//
// Key features:
// - SSH server with multi-user support and resource management
// - Handler system that routes requests to appropriate processors
// - Background services for scheduled jobs and continuous monitoring
// - Configurable connection limits and timeouts
// - Health checking and profiling support
// - Signal handling for graceful shutdown
//
// The server runs on port 2222 by default and supports both public key
// and password authentication depending on the user type.
package main

import (
	"context"
	"flag"
	"net/http"
	_ "net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/server"
	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/user"
	"github.com/mimecast/dtail/internal/version"
)

// main is the entry point for the DTail server daemon.
// It parses command-line arguments, sets up signal handling for graceful shutdown,
// initializes logging, and starts the SSH server. The function handles both
// timeout-based and signal-based shutdown scenarios.
func main() {
	var args config.Args
	var color bool
	var displayVersion bool
	var pprof string
	var shutdownAfter int

	user.NoRootCheck()

	flag.BoolVar(&color, "color", false, "Enable ANSII terminal colors")
	flag.BoolVar(&displayVersion, "version", false, "Display version")
	flag.IntVar(&args.SSHPort, "port", config.DefaultSSHPort, "SSH server port")
	flag.IntVar(&shutdownAfter, "shutdownAfter", 0, "Shutdown after so many seconds")
	flag.StringVar(&args.ConfigFile, "cfg", "", "Config file path")
	flag.StringVar(&args.LogDir, "logDir", "", "Log dir")
	flag.StringVar(&args.LogLevel, "logLevel", config.DefaultLogLevel, "Log level")
	flag.StringVar(&args.Logger, "logger", config.DefaultServerLogger, "Logger name")
	flag.StringVar(&args.SSHBindAddress, "bindAddress", "", "The SSH bind address")
	flag.StringVar(&pprof, "pprof", "", "Start PProf server this address")

	flag.Parse()
	args.NoColor = !color
	config.Setup(source.Server, &args, flag.Args())

	if displayVersion {
		version.PrintAndExit()
	}
	version.Print()

	ctx, cancel := context.WithCancel(context.Background())
	if shutdownAfter > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(shutdownAfter)*time.Second)
	}

	sigCh := make(chan os.Signal, 10)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	dlog.Start(ctx, &wg, source.Server)

	if pprof != "" {
		dlog.Server.Info("Starting PProf", pprof)
		go http.ListenAndServe(pprof, nil)
	}

	serv := server.New()
	status := serv.Start(ctx)
	cancel()

	wg.Wait()
	os.Exit(status)
}
