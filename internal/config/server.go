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
	// Enable in-memory auth-key registration and fast reconnect.
	AuthKeyEnabled bool `json:",omitempty"`
	// Auth-key cache entry TTL in seconds.
	AuthKeyTTLSeconds int `json:",omitempty"`
	// Maximum number of cached auth keys per user.
	AuthKeyMaxPerUser int `json:",omitempty"`
	// Retry interval for glob retries in milliseconds.
	ReadGlobRetryIntervalMs int `json:",omitempty"`
	// Retry interval for re-reading in tail/cat loops in milliseconds.
	ReadRetryIntervalMs int `json:",omitempty"`
	// Buffer size used for aggregate read channels.
	ReadAggregateLineBufferSize int `json:",omitempty"`
	// Delay after turbo processor flush/close to allow data transmission, in milliseconds.
	TurboTransmissionDelayMs int `json:",omitempty"`
	// Turbo EOF wait base duration in milliseconds.
	TurboEOFWaitBaseMs int `json:",omitempty"`
	// Turbo EOF wait per-file duration in milliseconds.
	TurboEOFWaitPerFileMs int `json:",omitempty"`
	// Maximum turbo EOF wait duration in milliseconds.
	TurboEOFWaitMaxMs int `json:",omitempty"`
	// Turbo channel buffer size.
	TurboChannelBufferSize int `json:",omitempty"`
	// Turbo channel flush timeout in milliseconds.
	TurboFlushTimeoutMs int `json:",omitempty"`
	// Turbo channel flush poll interval in milliseconds.
	TurboFlushPollIntervalMs int `json:",omitempty"`
	// Turbo read retry interval in milliseconds when data is expected but not yet available.
	TurboReadRetryIntervalMs int `json:",omitempty"`
	// Maximum time to wait for turbo EOF acknowledgement after signaling EOF, in milliseconds.
	TurboEOFAckTimeoutMs int `json:",omitempty"`
	// Wait for turbo aggregate serialization during shutdown in milliseconds.
	ShutdownTurboSerializeWaitMs int `json:",omitempty"`
	// Final idle recheck wait before shutdown in milliseconds.
	ShutdownIdleRecheckWaitMs int `json:",omitempty"`
	// Maximum size in bytes of a single command frame (bytes accumulated between
	// ';' delimiters). Frames that grow beyond this limit are rejected and the
	// session is closed to prevent unbounded memory exhaustion by a malicious or
	// misbehaving client. Default is 1 MiB.
	MaxCommandFrameSize int `json:",omitempty"`
	// Maximum number of glob expansion targets (file paths) that a single read
	// command is allowed to dispatch. When a glob pattern expands to more paths
	// than this limit, the excess paths are dropped and a warning is sent to the
	// client. This prevents an authenticated user with broad read permission from
	// spawning unbounded goroutines and exhausting server memory/CPU.
	// Default is 1000. Set to 0 to keep the built-in default.
	MaxGlobTargets int `json:",omitempty"`
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
		TurboBoostDisable:            false, // Default to false, meaning turbo boost is enabled by default
		AuthKeyEnabled:               true,
		AuthKeyTTLSeconds:            86400,
		AuthKeyMaxPerUser:            5,
		ReadGlobRetryIntervalMs:      5000,
		ReadRetryIntervalMs:          2000,
		ReadAggregateLineBufferSize:  10000,
		TurboTransmissionDelayMs:     50,
		TurboEOFWaitBaseMs:           500,
		TurboEOFWaitPerFileMs:        10,
		TurboEOFWaitMaxMs:            2000,
		TurboChannelBufferSize:       1000,
		TurboFlushTimeoutMs:          2000,
		TurboFlushPollIntervalMs:     10,
		TurboReadRetryIntervalMs:     1,
		TurboEOFAckTimeoutMs:         2000,
		ShutdownTurboSerializeWaitMs: 500,
		ShutdownIdleRecheckWaitMs:    10,
		MaxCommandFrameSize:          DefaultMaxCommandFrameSize,
		MaxGlobTargets:               1000,
	}
}

// NewDefaultServerConfigForTest returns a fresh ServerConfig populated with all
// default values. It is intended for use in unit tests that need to inspect or
// compare default configuration without running the full config initializer.
func NewDefaultServerConfigForTest() *ServerConfig {
	return newDefaultServerConfig()
}

// UserPermissions retrieves the permission set of a given user.
func (c *ServerConfig) UserPermissions(userName string) (permissions []string, err error) {
	if c == nil {
		return nil, errors.New("missing server config")
	}

	permissions = c.Permissions.Default
	if p, ok := c.Permissions.Users[userName]; ok {
		permissions = p
	}
	if len(permissions) == 0 {
		err = errors.New("Empty set of permission, user won't be able to open any files")
	}
	return
}

// ServerUserPermissions retrieves the permission set of a given user.
func ServerUserPermissions(userName string) (permissions []string, err error) {
	if Server == nil {
		return nil, errors.New("missing server config")
	}
	return Server.UserPermissions(userName)
}
