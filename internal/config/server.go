package config

import (
	"errors"
)

// Permissions map. Each SSH user has a list of permissions which log files it
// is allowed to follow and which ones not.
type Permissions struct {
	// The default user permissions.
	Default []string
	// The per user special permissions.
	Users map[string][]string
}

// JobCommons summarises common job fields
type jobCommons struct {
	Name      string
	Enable    bool
	Files     string
	Query     string
	Outfile   string   `json:",omitempty"`
	Discovery string   `json:",omitempty"`
	Servers   []string `json:",omitempty"`
	AllowFrom []string `json:",omitempty"`
}

// Scheduled allows to configure scheduled mapreduce jobs.
type Scheduled struct {
	jobCommons
	TimeRange [2]int
}

// Continuous allows to configure continuous running mapreduce jobs.
type Continuous struct {
	jobCommons
	RestartOnDayChange bool `json:",omitempty"`
}

// ServerConfig represents the server configuration.
type ServerConfig struct {
	// The SSH server bind port.
	SSHBindAddress string
	// The max amount of concurrent user connection allowed to connect to the server.
	MaxConnections int
	// The max amount of concurrent cats per server.
	MaxConcurrentCats int
	// The max amount of concurrent tails per server.
	MaxConcurrentTails int
	// The max line length until it's split up into multiple smaller lines.
	MaxLineLength int
	// The user permissions.
	Permissions Permissions `json:",omitempty"`
	// The mapr log format
	MapreduceLogFormat string `json:",omitempty"`
	// The default path of the server host key
	HostKeyFile string
	// The host key size in bits
	HostKeyBits int
	// Scheduled mapreduce jobs.
	Schedule []Scheduled `json:",omitempty"`
	// Continuous mapreduce jobs
	Continuous []Continuous `json:",omitempty"`
	// The allowed key exchanges algorithms.
	KeyExchanges []string `json:",omitempty"`
	// The allowed cipher algorithms.
	Ciphers []string `json:",omitempty"`
	// The allowed MAC algorithms.
	MACs []string `json:",omitempty"`
	// Disable turbo boost mode. When set to true, disables the optimized file processing mode.
	// By default, turbo boost is enabled for cat/grep/tail and MapReduce operations, providing
	// better performance through direct writing that bypasses internal channels.
	// Set this to true only if you experience issues with turbo boost mode.
	TurboBoostDisable bool `json:",omitempty"`
}

// Create a new default server configuration.
func newDefaultServerConfig() *ServerConfig {
	defaultPermissions := []string{"^/.*"}
	defaultBindAddress := "0.0.0.0"
	return &ServerConfig{
		HostKeyBits:        4096,
		HostKeyFile:        "./cache/ssh_host_key",
		MapreduceLogFormat: "default",
		MaxConcurrentCats:  2,
		MaxConcurrentTails: 50,
		MaxConnections:     10,
		MaxLineLength:      1024 * 1024,
		SSHBindAddress:     defaultBindAddress,
		Permissions: Permissions{
			Default: defaultPermissions,
		},
		TurboBoostDisable:  false, // Default to false, meaning turbo boost is enabled by default
	}
}

// ServerUserPermissions retrieves the permission set of a given user.
func ServerUserPermissions(userName string) (permissions []string, err error) {
	permissions = Server.Permissions.Default
	if p, ok := Server.Permissions.Users[userName]; ok {
		permissions = p
	}
	if len(permissions) == 0 {
		err = errors.New("Empty set of permission, user won't be able to open any files")
	}
	return
}
