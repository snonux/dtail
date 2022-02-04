package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	_ "net/http"
	_ "net/http/pprof"
	"os"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/signal"
	"github.com/mimecast/dtail/internal/omode"
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
	var pprof string
	var shutdownAfter int

	userName := user.Name()

	flag.BoolVar(&args.NoColor, "noColor", false, "Disable ANSII terminal colors")
	flag.BoolVar(&args.Quiet, "quiet", false, "Quiet output mode")
	flag.BoolVar(&args.RegexInvert, "invert", false, "Invert regex")
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
	flag.IntVar(&args.SSHPort, "port", config.DefaultSSHPort, "SSH server port")
	flag.IntVar(&args.Timeout, "timeout", 0, "Max time dtail server will collect data until disconnection")
	flag.IntVar(&shutdownAfter, "shutdownAfter", 3600*24, "Shutdown after so many seconds")
	flag.StringVar(&args.ConfigFile, "cfg", "", "Config file path")
	flag.StringVar(&args.Discovery, "discovery", "", "Server discovery method")
	flag.StringVar(&args.LogDir, "logDir", "~/log", "Log dir")
	flag.StringVar(&args.Logger, "logger", config.DefaultClientLogger, "Logger name")
	flag.StringVar(&args.LogLevel, "logLevel", config.DefaultLogLevel, "Log level")
	flag.StringVar(&args.SSHPrivateKeyFilePath, "key", "", "Path to private key")
	flag.StringVar(&args.QueryStr, "query", "", "Map reduce query")
	flag.StringVar(&args.RegexStr, "regex", ".", "Regular expression")
	flag.StringVar(&args.ServersStr, "servers", "", "Remote servers to connect")
	flag.StringVar(&args.UserName, "user", userName, "Your system user name")
	flag.StringVar(&args.What, "files", "", "File(s) to read")
	flag.StringVar(&grep, "grep", "", "Alias for -regex")
	flag.StringVar(&pprof, "pprof", "", "Start PProf server this address")

	flag.Parse()
	if grep != "" {
		args.RegexStr = grep
	}
	config.Setup(source.Client, &args, flag.Args())
	if displayVersion {
		version.PrintAndExit()
	}
	if !args.Plain {
		if displayWideColorTable {
			color.TablePrintAndExit(true)
		}
		if displayColorTable {
			color.TablePrintAndExit(false)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	if shutdownAfter > 0 {
		// NEXT: This does not work (auto shutdown)
		ctx, cancel = context.WithTimeout(ctx, time.Duration(shutdownAfter)*time.Second)
		defer cancel()
	}

	var wg sync.WaitGroup
	wg.Add(1)
	dlog.Start(ctx, &wg, source.Client)

	if checkHealth {
		fmt.Println("WARN: DTail health check has moved to separate binary dtailhealth" +
			" - please adjust the monitoring scripts!")
		cancel()
		os.Exit(1)
	}

	if pprof != "" {
		go http.ListenAndServe(pprof, nil)
		dlog.Client.Info("Started PProf", pprof)
	}

	var client clients.Client
	var err error
	args.Mode = omode.TailClient

	switch args.QueryStr {
	case "":
		if client, err = clients.NewTailClient(args); err != nil {
			panic(err)
		}
	default:
		if client, err = clients.NewMaprClient(args, clients.DefaultMode); err != nil {
			panic(err)
		}
	}

	status := client.Start(ctx, signal.InterruptCh(ctx))
	cancel()

	wg.Wait()
	os.Exit(status)
}
