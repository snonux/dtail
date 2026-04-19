package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mimecast/dtail/internal/cli"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/server"
	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/user"
	"github.com/mimecast/dtail/internal/version"
)

// The evil begins here.
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
		runtimeCfg := config.CurrentRuntime()
		version.PrintAndExit(runtimeCfg.Client != nil && runtimeCfg.Client.TermColorsEnable)
	}
	version.Print(false)

	// rootCtx is always cancelled on exit to ensure the internal goroutine
	// spawned by context.WithCancel is released. When -shutdownAfter is set,
	// ctx is replaced by a child WithTimeout context whose own cancel is also
	// deferred, preventing the lostcancel leak flagged by go vet.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	ctx := rootCtx
	cancel := context.CancelFunc(rootCancel)

	if shutdownAfter > 0 {
		// Override ctx with a timeout-bounded child; defer its cancel so the
		// timeout goroutine is always cleaned up regardless of code path.
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(rootCtx, time.Duration(shutdownAfter)*time.Second)
		defer timeoutCancel()
		// Callers that invoke cancel() (e.g. the signal handler and post-serve
		// cleanup) should trigger the timeout cancel so the server shuts down
		// promptly even before the deadline fires.
		cancel = timeoutCancel
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

	var pprofServer *cli.PProfServer
	if pprof != "" {
		pprofServer, pprofErr := cli.NewPProfServer(pprof)
		if pprofErr != nil {
			dlog.Client.Error("Unable to start PProf", pprofErr)
		} else {
			dlog.Client.Info("Starting PProf", pprofServer.Address())
			pprofServer.Start(nil)
		}
	}

	serv := server.New(config.CurrentRuntime())
	status := serv.Start(ctx)
	cancel()
	if pprofServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := pprofServer.Shutdown(shutdownCtx); err != nil {
			dlog.Client.Error("Unable to stop PProf", err)
		}
		shutdownCancel()
	}

	wg.Wait()
	os.Exit(status)
}
