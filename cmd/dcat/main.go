package main

import (
	"flag"
	"os"

	"github.com/mimecast/dtail/internal/cli"
	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
)

func main() {
	os.Exit(run())
}

func run() int {
	var args config.Args
	runner := cli.BindCommonClientFlags(flag.CommandLine, &args)
	return runner.RunClient("dcat", func(args config.Args) (clients.Client, error) {
		return clients.NewCatClient(args)
	})
}
