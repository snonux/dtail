package constants

// Channel buffer size constants
const (
	// DefaultLinesChannelSize is the default buffer size for lines channels
	DefaultLinesChannelSize = 100

	// DefaultServerMessagesChannelSize is the default buffer size for server messages
	DefaultServerMessagesChannelSize = 10

	// LoggerBufferChannelSize is the buffer size for logger channels
	// Calculated as runtime.NumCPU() * 100 at runtime
	LoggerBufferChannelMultiplier = 100
)