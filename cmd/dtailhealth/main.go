// Package main provides the DTail Health Check utility.
// DTailHealth is a specialized tool for monitoring the health and availability
// of DTail servers. It connects to servers and performs basic connectivity
// and functionality tests to ensure they are operating correctly.
//
// Key features:
// - Server connectivity testing via SSH
// - Basic functionality verification
// - Minimal logging output (suitable for monitoring scripts)
// - Single server health checking
// - Exit codes suitable for monitoring systems
// - Built-in profiling support for diagnostics
//
// DTailHealth is typically used by monitoring systems like Nagios, Zabbix,
// or custom health check scripts to verify that DTail servers are responding
// and functioning properly. It was separated from the main dtail binary
// to provide a lightweight, focused health checking tool.
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
	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/version"
)

// main is the entry point for the DTail Health Check utility.
// It parses command-line arguments, initializes minimal logging,
// creates a HealthClient, and performs a health check against the
// specified server. The function exits with appropriate status codes
// for use in monitoring systems.
func main() {
	var args config.Args
	var displayVersion bool
	var pprof string

	flag.BoolVar(&displayVersion, "version", false, "Display version")
	flag.StringVar(&args.Logger, "logger", config.DefaultHealthCheckLogger, "Logger name")
	flag.StringVar(&args.LogLevel, "logLevel", "none", "Log level")
	flag.StringVar(&args.ServersStr, "server", "", "Remote server to connect")
	flag.StringVar(&pprof, "pprof", "", "Start PProf server this address")
	flag.Parse()

	if displayVersion {
		version.PrintAndExit()
	}

	config.Setup(source.HealthCheck, &args, flag.Args())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	dlog.Start(ctx, &wg, source.HealthCheck)

	if pprof != "" {
		go http.ListenAndServe(pprof, nil)
		dlog.Client.Info("Started PProf", pprof)
	}

	healthClient, _ := clients.NewHealthClient(args)
	os.Exit(healthClient.Start(ctx, signal.NoCh(ctx)))
}
