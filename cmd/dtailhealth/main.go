package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/cli"
	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/signal"
	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/version"
)

// The evil begins here.
func main() {
	var args config.Args
	var displayVersion bool
	var pprof string

	flag.BoolVar(&displayVersion, "version", false, "Display version")
	flag.StringVar(&args.Logger, "logger", config.DefaultHealthCheckLogger, "Logger name")
	flag.StringVar(&args.LogLevel, "logLevel", "none", "Log level")
	flag.StringVar(&args.ServersStr, "server", "", "Remote server to connect")
	flag.BoolVar(&args.NoAuthKey, "no-auth-key", false, "Disable auth-key fast reconnect feature")
	flag.StringVar(&pprof, "pprof", "", "Start PProf server this address")
	flag.Parse()

	if displayVersion {
		version.PrintAndExit(false)
	}

	config.Setup(source.HealthCheck, &args, flag.Args())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	dlog.Start(ctx, &wg, source.HealthCheck)

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

	healthClient, err := clients.NewHealthClient(args)
	status := 0
	if err != nil {
		fmt.Fprintf(os.Stderr, "CRITICAL: unable to create dtailhealth client: %v\n", err)
		status = 2
	} else {
		status = healthClient.Start(ctx, signal.NoCh(ctx))
	}

	if pprofServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := pprofServer.Shutdown(shutdownCtx); err != nil {
			dlog.Client.Error("Unable to stop PProf", err)
		}
		shutdownCancel()
	}

	cancel()
	wg.Wait()
	os.Exit(status)
}
