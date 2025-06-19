package constants

// Numeric limits and configuration values
const (
	// MaxFlushRetries is the maximum number of flush retry attempts
	MaxFlushRetries = 10

	// MaxReadCommandRetries is the maximum number of read command retries
	MaxReadCommandRetries = 5

	// DefaultConnectionsPerCPU is the default number of connections per CPU
	DefaultConnectionsPerCPU = 10

	// DefaultSSHPort is the default SSH server port
	DefaultSSHPort = 2222

	// InterruptTimeoutSeconds is the timeout for interrupt handling
	InterruptTimeoutSeconds = 3

	// MaxConcurrentCats is the maximum number of concurrent cat operations
	MaxConcurrentCats = 2

	// MaxConcurrentTails is the maximum number of concurrent tail operations
	MaxConcurrentTails = 50

	// MaxConnections is the maximum total number of connections
	MaxConnections = 10

	// HostKeyBits is the number of bits for SSH host keys
	HostKeyBits = 4096

	// DefaultMaxLineLength is the default maximum line length (1KB)
	DefaultMaxLineLength = 1024

	// ServerMaxLineLength is the server maximum line length (1MB)
	ServerMaxLineLength = 1024 * 1024

	// MaxSymlinkDepth is the maximum depth for following symlinks
	MaxSymlinkDepth = 100

	// DefaultMapReduceRowsLimit is the default limit for MapReduce output rows
	DefaultMapReduceRowsLimit = 10

	// MapReduceUnlimited indicates no limit on MapReduce rows
	MapReduceUnlimited = -1

	// HealthOKStatus is the status code for healthy service
	HealthOKStatus = 0

	// HealthCriticalStatus is the status code for critical health issues
	HealthCriticalStatus = 2

	// PercentageMultiplier is used for percentage calculations
	PercentageMultiplier = 100.0
)