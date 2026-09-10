package main

import (
	"flag"
	"os"

	"github.com/mimecast/dtail/internal/cli"
	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
)

func main() {
	os.Exit(run())
}

func run() int {
	args := config.Args{Mode: omode.MapClient}
	runner := cli.BindCommonClientFlags(flag.CommandLine, &args)
	flag.IntVar(&args.Timeout, "timeout", 0, "Max time dtail server will collect data until disconnection")
	flag.StringVar(&args.QueryStr, "query", "", "Map reduce query")
	return runner.RunClient("dmap", func(args config.Args) (clients.Client, error) {
		return clients.NewMaprClient(args, clients.DefaultMode)
	})
}
