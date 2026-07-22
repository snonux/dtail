package config

// RuntimeConfig contains the active runtime configuration for a process.
// It is intended to be injected into components instead of relying on package globals.
type RuntimeConfig struct {
	Client *ClientConfig
	Server *ServerConfig
	Common *CommonConfig
}

// CurrentRuntime returns the currently initialized runtime configuration.
func CurrentRuntime() RuntimeConfig {
	return RuntimeConfig{
		Client: Client,
		Server: Server,
		Common: Common,
	}
}
