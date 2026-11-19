package main

import (
	"flag"
	"os"

	"github.com/mimecast/dtail/internal/cli"
	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

func main() {
	os.Exit(run())
}

func run() int {
	args := config.Args{Mode: omode.MapClient}
	runner := cli.BindCommonClientFlags(flag.CommandLine, &args)
	runner.WithBrushFactory(brush.New)
	flag.IntVar(&args.Timeout, "timeout", 0, "Max time dtail server will collect data until disconnection")
	flag.StringVar(&args.QueryStr, "query", "", "Map reduce query (without one, on a terminal, the query builder opens)")
	// Without a query, dmap opens its query builder where it runs on a
	// terminal (cli.InteractiveDefault.ResolveQueryBuilder); elsewhere the
	// missing query stays the error it was. Decided after the setup, which
	// takes a positional argument for the query (dmap 'from ...' FILE).
	builderDefault := cli.BindQueryBuilderDefault(flag.CommandLine)
	var queryBuilder bool
	runner.BeforeRuntime(func(args *config.Args) (bool, int) {
		queryBuilder = builderDefault.ResolveQueryBuilder(flag.CommandLine, args)
		return false, 0
	})
	return runner.RunClientWithBrush("dmap", func(args config.Args, cfg config.RuntimeConfig,
		loggers clients.LoggerDependencies, colorizer *brush.Brush) (clients.Client, error) {
		if queryBuilder {
			return clients.NewQueryBuilderClient(args, cfg, loggers, colorizer)
		}
		return clients.NewMaprClient(args, cfg, clients.DefaultMode, loggers, colorizer)
	})
}
