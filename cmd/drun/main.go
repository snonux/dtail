package main

import (
	"context"
	"flag"
	"io/ioutil"
	"os"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/logger"
	"github.com/mimecast/dtail/internal/user"
	"github.com/mimecast/dtail/internal/version"
)

// The evil begins here.
func main() {
	var cfgFile string
	var command string
	var connectionsPerCPU int
	var debugEnable bool
	var discovery string
	var displayVersion bool
	var noColor bool
	var serversStr string
	var silentEnable bool
	var sshPort int
	var timeout int
	var trustAllHosts bool

	userName := user.Name()

	flag.BoolVar(&debugEnable, "debug", false, "Activate debug messages")
	flag.BoolVar(&displayVersion, "version", false, "Display version")
	flag.BoolVar(&noColor, "noColor", false, "Disable ANSII terminal colors")
	flag.BoolVar(&silentEnable, "silent", false, "Reduce output")
	flag.BoolVar(&trustAllHosts, "trustAllHosts", false, "Auto trust all unknown host keys")
	flag.IntVar(&connectionsPerCPU, "cpc", 10, "How many connections established per CPU core concurrently")
	flag.IntVar(&sshPort, "port", 2222, "SSH server port")
	flag.IntVar(&timeout, "timeout", 0, "Command execution timeout")
	flag.StringVar(&cfgFile, "cfg", "", "Config file path")
	flag.StringVar(&command, "command", "", "Command to run")
	flag.StringVar(&discovery, "discovery", "", "Server discovery method")
	flag.StringVar(&serversStr, "servers", "", "Remote servers to connect")
	flag.StringVar(&userName, "user", userName, "Your system user name")

	flag.Parse()

	config.Read(cfgFile, sshPort)
	color.Colored = !noColor

	if displayVersion {
		version.PrintAndExit()
	}

	ctx := context.Background()
	serverEnable := false

	logger.Start(ctx, serverEnable, debugEnable, silentEnable, silentEnable)

	args := clients.Args{
		ConnectionsPerCPU: connectionsPerCPU,
		ServersStr:        serversStr,
		Discovery:         discovery,
		UserName:          userName,
		What:              readCommand(command),
		TrustAllHosts:     trustAllHosts,
		Timeout:           timeout,
	}

	client, err := clients.NewRunClient(args)
	if err != nil {
		panic(err)
	}

	status := client.Start(ctx)
	logger.Flush()
	os.Exit(status)
}

func readCommand(command string) string {
	if _, err := os.Stat(command); os.IsNotExist(err) {
		return command
	}

	bytes, err := ioutil.ReadFile(command)
	if err != nil {
		panic(err)
	}

	return string(bytes)
}
