package handlers

import (
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/mapr/server"
)

type readCommandContext interface {
	LogContext() interface{}
}

type readCommandFiles interface {
	PrepareReadTarget(path string) (fs.ValidatedReadTarget, bool)
	CatLimiter() chan struct{}
	TailLimiter() chan struct{}
}

type readCommandMessages interface {
	SendServerMessage(message string)
	ServerMessagesChannel() chan string
	Hostname() string
	PlainOutput() bool
	Serverless() bool
}

type readCommandAggregates interface {
	Aggregate() *server.Aggregate
}

type readCommandLifecycle interface {
	AddPendingFiles(delta int32) int32
	CompletePendingFile() (remaining int32, activeCommands int32)
	PendingAndActive() (pending int32, activeCommands int32)
	ActiveSessionGeneration() uint64
	TriggerShutdown()
}

type readCommandOutput interface {
	DirectOutputActive() bool
	// EnableDirectOutput atomically enables output mode; it returns true when this
	// call performed the off->on transition and false when it was already on.
	EnableDirectOutput() bool
	HasOutputEOF() bool
	FlushOutput()
	// OutputEpoch returns the output handshake epoch; capture it before the
	// pending-work check and pass it to SignalOutputEOF (see baseHandler).
	OutputEpoch() uint64
	// SignalOutputEOF drops the signal when the epoch is no longer current.
	SignalOutputEOF(epoch uint64)
	GetOutputChannel() chan []byte
	OutputChannelLen() int
	WaitForOutputEOFAck(timeout time.Duration) bool
}

type readCommandTiming interface {
	ReadGlobRetryInterval() time.Duration
	ReadRetryInterval() time.Duration
	MaxLineLength() int
	OutputTransmissionDelay() time.Duration
	OutputEOFWaitDuration(fileCount int) time.Duration
	ShutdownSerializeWait() time.Duration
	ShutdownIdleRecheckWait() time.Duration
	OutputEOFAckTimeout() time.Duration
	// MaxGlobTargets returns the maximum number of file paths a single glob
	// expansion may produce before excess paths are dropped. This caps the
	// number of goroutines and memory consumed per read command.
	MaxGlobTargets() int
}

type readCommandServer interface {
	readCommandContext
	readCommandFiles
	readCommandMessages
	readCommandAggregates
	readCommandLifecycle
	readCommandOutput
	readCommandTiming
}

var _ readCommandServer = (*ServerHandler)(nil)

// LogContext returns the logger context associated with the current user/session.
func (h *ServerHandler) LogContext() interface{} {
	return h.user
}

// SendServerMessage sends a formatted server message to the client.
func (h *ServerHandler) SendServerMessage(message string) {
	h.sendln(h.serverMessages, message)
}

// PrepareReadTarget validates the current user's access to the given path.
func (h *ServerHandler) PrepareReadTarget(path string) (fs.ValidatedReadTarget, bool) {
	return h.user.ValidateReadTarget(path, "readfiles")
}

// ServerMessagesChannel returns the server message channel.
func (h *ServerHandler) ServerMessagesChannel() chan string {
	return h.serverMessages
}

// CatLimiter returns the concurrency limiter for cat/grep style reads.
func (h *ServerHandler) CatLimiter() chan struct{} {
	return h.catLimiter
}

// TailLimiter returns the concurrency limiter for tail reads.
func (h *ServerHandler) TailLimiter() chan struct{} {
	return h.tailLimiter
}

// Hostname returns the short hostname used for response formatting.
func (h *ServerHandler) Hostname() string {
	return h.hostname
}

// PlainOutput reports whether plain output mode is enabled.
func (h *ServerHandler) PlainOutput() bool {
	return h.plain
}

// Serverless reports whether the current session is running in serverless mode.
func (h *ServerHandler) Serverless() bool {
	return h.serverless
}

// Aggregate returns the MapReduce aggregate if enabled for the session.
// Uses the atomic accessor to avoid a race with concurrent handleMapCommand writes.
func (h *ServerHandler) Aggregate() *server.Aggregate {
	return h.getAggregate()
}

// AddPendingFiles increments or decrements the pending file counter.
func (h *ServerHandler) AddPendingFiles(delta int32) int32 {
	return atomic.AddInt32(&h.pendingFiles, delta)
}

// CompletePendingFile marks one file as completed and returns pending/active counters.
func (h *ServerHandler) CompletePendingFile() (remaining int32, activeCommands int32) {
	remaining = atomic.AddInt32(&h.pendingFiles, -1)
	activeCommands = atomic.LoadInt32(&h.activeCommands)
	return remaining, activeCommands
}

// PendingAndActive returns the current pending file and active command counts.
func (h *ServerHandler) PendingAndActive() (pending int32, activeCommands int32) {
	pending = atomic.LoadInt32(&h.pendingFiles)
	activeCommands = atomic.LoadInt32(&h.activeCommands)
	return pending, activeCommands
}

// ActiveSessionGeneration returns the currently active interactive session generation.
func (h *ServerHandler) ActiveSessionGeneration() uint64 {
	return h.sessionState.currentGeneration()
}

// TriggerShutdown starts the handler shutdown sequence.
func (h *ServerHandler) TriggerShutdown() {
	h.shutdown()
}

// FlushOutput drains pending output data to the underlying writer.
func (h *ServerHandler) FlushOutput() {
	h.flushOutput()
}

// OutputEOFAckTimeout returns the timeout used while waiting for output EOF ACK.
func (h *ServerHandler) OutputEOFAckTimeout() time.Duration {
	return durationFromMilliseconds(h.serverCfg.OutputEOFAckTimeoutMs, 2*time.Second)
}

func durationFromMilliseconds(value int, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}

func positiveIntOrDefault(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// ReadGlobRetryInterval returns the retry interval for glob expansion failures.
func (h *ServerHandler) ReadGlobRetryInterval() time.Duration {
	return durationFromMilliseconds(h.serverCfg.ReadGlobRetryIntervalMs, 5*time.Second)
}

// ReadRetryInterval returns the retry interval for repeated file reads.
func (h *ServerHandler) ReadRetryInterval() time.Duration {
	return durationFromMilliseconds(h.serverCfg.ReadRetryIntervalMs, 2*time.Second)
}

// MaxLineLength returns the configured max line length for file readers.
func (h *ServerHandler) MaxLineLength() int {
	return positiveIntOrDefault(h.serverCfg.MaxLineLength, 1024*1024)
}

// OutputTransmissionDelay returns the delay used after output flushes.
func (h *ServerHandler) OutputTransmissionDelay() time.Duration {
	return durationFromMilliseconds(h.serverCfg.OutputTransmissionDelayMs, 50*time.Millisecond)
}

// OutputEOFWaitDuration returns the wait duration used before signaling output EOF.
func (h *ServerHandler) OutputEOFWaitDuration(fileCount int) time.Duration {
	baseWait := durationFromMilliseconds(h.serverCfg.OutputEOFWaitBaseMs, 500*time.Millisecond)
	if fileCount <= 10 {
		return baseWait
	}

	perFileWait := durationFromMilliseconds(h.serverCfg.OutputEOFWaitPerFileMs, 10*time.Millisecond)
	maxWait := durationFromMilliseconds(h.serverCfg.OutputEOFWaitMaxMs, 2*time.Second)
	wait := time.Duration(fileCount) * perFileWait
	if wait > maxWait {
		return maxWait
	}
	return wait
}

// ShutdownSerializeWait returns the wait before final output shutdown checks.
func (h *ServerHandler) ShutdownSerializeWait() time.Duration {
	return durationFromMilliseconds(h.serverCfg.ShutdownOutputSerializeWaitMs, 500*time.Millisecond)
}

// ShutdownIdleRecheckWait returns the wait used for the final idle recheck.
func (h *ServerHandler) ShutdownIdleRecheckWait() time.Duration {
	return durationFromMilliseconds(h.serverCfg.ShutdownIdleRecheckWaitMs, 10*time.Millisecond)
}

// MaxGlobTargets returns the maximum number of paths a glob may expand to.
// Excess paths beyond the cap are silently dropped (with a warning logged)
// to prevent goroutine/memory exhaustion from a broad read permission glob.
func (h *ServerHandler) MaxGlobTargets() int {
	return positiveIntOrDefault(h.serverCfg.MaxGlobTargets, 1000)
}

func (h *ServerHandler) outputManagerConfig() outputManagerConfig {
	return outputManagerConfig{
		channelBufferSize: positiveIntOrDefault(h.serverCfg.OutputChannelBufferSize, defaultOutputChannelBufferSize),
		flushTimeout:      durationFromMilliseconds(h.serverCfg.OutputFlushTimeoutMs, defaultOutputFlushTimeout),
		flushPollInterval: durationFromMilliseconds(h.serverCfg.OutputFlushPollIntervalMs, defaultOutputFlushPollInterval),
		readRetryInterval: durationFromMilliseconds(h.serverCfg.OutputReadRetryIntervalMs, defaultOutputReadRetryInterval),
		eofAckQuietPeriod: durationFromMilliseconds(h.serverCfg.OutputTransmissionDelayMs, defaultOutputEOFAckQuietPeriod),
	}
}
