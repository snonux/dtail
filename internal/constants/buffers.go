package constants

// Buffer size constants in bytes
const (
	// StatsArraySize is the size of arrays for tracking stats
	StatsArraySize = 100

	// LineBufferInitialCapacity is the initial capacity for line buffers (8KB)
	LineBufferInitialCapacity = 8192

	// ReadBufferSize is the size of read buffers (8KB)
	ReadBufferSize = 8192

	// DefaultChunkSize is the default chunk size for reading (64KB)
	DefaultChunkSize = 64 * 1024

	// InitialBufferSize is the initial buffer size for processors (64KB)
	InitialBufferSize = 64 * 1024
)