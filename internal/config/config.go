// Package config provides configuration management for DTail clients and servers.
// It handles hierarchical configuration from multiple sources including configuration
// files, environment variables, and command-line arguments with proper precedence.
//
// The configuration system supports:
// - Common configuration shared between client and server
// - Client-specific configuration for connection and behavior settings
// - Server-specific configuration for SSH server and resource management
// - Environment variable overrides with DTAIL_ prefix
// - JSON configuration file support
// - Command-line argument parsing and validation
//
// Configuration precedence (highest to lowest):
// 1. Command-line arguments
// 2. Environment variables
// 3. Configuration file
// 4. Default values
package config

import (
	"github.com/mimecast/dtail/internal/constants"
	"github.com/mimecast/dtail/internal/source"
)

const (
	// HealthUser is used for the health check
	HealthUser string = "DTAIL-HEALTH"
	// ScheduleUser is used for non-interactive scheduled mapreduce queries.
	ScheduleUser string = "DTAIL-SCHEDULE"
	// ContinuousUser is used for non-interactive continuous mapreduce queries.
	ContinuousUser string = "DTAIL-CONTINUOUS"
	// InterruptTimeoutS specifies the Ctrl+C log pause interval.
	InterruptTimeoutS int = constants.InterruptTimeoutSeconds
	// DefaultConnectionsPerCPU controls how many connections are established concurrently.
	DefaultConnectionsPerCPU int = constants.DefaultConnectionsPerCPU
	// DefaultSSHPort is the default DServer port.
	DefaultSSHPort int = constants.DefaultSSHPort
	// DefaultLogLevel specifies the default log level (obviously)
	DefaultLogLevel string = "info"
	// DefaultClientLogger specifies the default logger for the client commands.
	DefaultClientLogger string = "fout"
	// DefaultServerLogger specifies the default logger for dtail server.
	DefaultServerLogger string = "file"
	// DefaultHealthCheckLogger specifies the default logger used for health checks.
	DefaultHealthCheckLogger string = "none"
)

// Client holds a DTail client configuration.
// This global variable provides access to client-specific settings
// after configuration initialization.
var Client *ClientConfig

// Server holds a DTail server configuration.
// This global variable provides access to server-specific settings
// after configuration initialization.
var Server *ServerConfig

// Common holds common configs of both both, client and server.
// This global variable provides access to shared configuration
// settings used by both client and server components.
var Common *CommonConfig

// Setup initializes the DTail configuration from multiple sources.
// It creates default configurations, parses configuration files,
// applies environment variables, processes command-line arguments,
// and makes the final configuration available via global variables.
//
// Parameters:
//   - sourceProcess: The type of process (client, server, health check)
//   - args: Parsed command-line arguments
//   - additionalArgs: Additional arguments from flag.Args()
//
// This function panics on configuration errors to ensure the application
// cannot start with invalid configuration.
func Setup(sourceProcess source.Source, args *Args, additionalArgs []string) {
	initializer := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}
	if err := initializer.parseConfig(args); err != nil {
		panic(err)
	}
	if err := initializer.transformConfig(sourceProcess, args, additionalArgs); err != nil {
		panic(err)
	}

	// Make config accessible globally
	Server = initializer.Server
	Client = initializer.Client
	Common = initializer.Common
}
