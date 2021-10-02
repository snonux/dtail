package config

const (
	// ControlUser is used for various DTail specific operations.
	ControlUser string = "DTAIL-CONTROL"
	// ScheduleUser is used for non-interactive scheduled mapreduce queries.
	ScheduleUser string = "DTAIL-SCHEDULE"
	// ContinuousUser is used for non-interactive continuous mapreduce queries.
	ContinuousUser string = "DTAIL-CONTINUOUS"
	// InterruptTimeoutS is used to terminate DTail when Ctrl+C was pressed twice within a given interval.
	InterruptTimeoutS int = 3
	// ConnectionsPerCPU controls how many connections are established concurrently as a start (slow start)
	DefaultConnectionsPerCPU int = 10
	// DTailSSHServerDefaultPort is the default DServer port.
	DefaultSSHPort int = 2222
)

// Client holds a DTail client configuration.
var Client *ClientConfig

// Server holds a DTail server configuration.
var Server *ServerConfig

// Common holds common configs of both both, client and server.
var Common *CommonConfig

// Setup the DTail configuration.
func Setup(args *Args, additionalArgs []string) {
	initializer := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}
	initializer.parseConfig(args)
	Client, Server, Common = initializer.transformConfig(
		args, additionalArgs,
		initializer.Client,
		initializer.Server,
		initializer.Common,
	)
}
