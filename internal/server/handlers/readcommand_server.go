package handlers

import (
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/mapr/server"
)

type readCommandContext interface {
	LogContext() interface{}
}

type readCommandFiles interface {
	CanReadFile(path string) bool
	CatLimiter() chan struct{}
	TailLimiter() chan struct{}
}

type readCommandMessages interface {
	SendServerMessage(message string)
	ServerMessagesChannel() chan string
	Hostname() string
	PlainOutput() bool
	Serverless() bool
	RegisterAggregateLines(lines chan *line.Line)
	SharedLinesChannel() chan *line.Line
}

type readCommandAggregates interface {
	HasRegularAggregate() bool
	TurboAggregate() *server.TurboAggregate
}

type readCommandLifecycle interface {
	AddPendingFiles(delta int32) int32
	CompletePendingFile() (remaining int32, activeCommands int32)
	PendingAndActive() (pending int32, activeCommands int32)
	ActiveSessionGeneration() uint64
	TriggerShutdown()
}

type readCommandTurbo interface {
	TurboBoostDisabled() bool
	IsTurboMode() bool
	EnableTurboMode()
	HasTurboEOF() bool
	FlushTurboData()
	SignalTurboEOF()
	GetTurboChannel() chan []byte
	TurboChannelLen() int
	WaitForTurboEOFAck(timeout time.Duration) bool
}

type readCommandTiming interface {
	ReadGlobRetryInterval() time.Duration
	ReadRetryInterval() time.Duration
	MaxLineLength() int
	AggregateLinesChannelBufferSize() int
	TurboDataTransmissionDelay() time.Duration
	TurboEOFWaitDuration(fileCount int) time.Duration
	ShutdownTurboSerializeWait() time.Duration
	ShutdownIdleRecheckWait() time.Duration
	TurboEOFAckTimeout() time.Duration
}

type readCommandServer interface {
	readCommandContext
	readCommandFiles
	readCommandMessages
	readCommandAggregates
	readCommandLifecycle
	readCommandTurbo
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

// CanReadFile reports whether the current user can read the given path.
func (h *ServerHandler) CanReadFile(path string) bool {
	return h.user.HasFilePermission(path, "readfiles")
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

// TurboBoostDisabled reports whether turbo mode is disabled by configuration.
func (h *ServerHandler) TurboBoostDisabled() bool {
	return h.serverCfg.TurboBoostDisable
}

// HasRegularAggregate reports whether the regular map-reduce aggregate is active.
func (h *ServerHandler) HasRegularAggregate() bool {
	return h.aggregate != nil
}

// RegisterAggregateLines attaches a file line channel to the active aggregate.
func (h *ServerHandler) RegisterAggregateLines(lines chan *line.Line) {
	if h.aggregate != nil {
		h.aggregate.NextLinesCh <- lines
	}
}

// SharedLinesChannel returns the shared outbound line channel.
func (h *ServerHandler) SharedLinesChannel() chan *line.Line {
	return h.lines
}

// TurboAggregate returns the turbo aggregate if enabled for the session.
func (h *ServerHandler) TurboAggregate() *server.TurboAggregate {
	return h.turboAggregate
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

// FlushTurboData drains pending turbo data to the underlying writer.
func (h *ServerHandler) FlushTurboData() {
	h.flushTurboData()
}

// TurboEOFAckTimeout returns the timeout used while waiting for turbo EOF ACK.
func (h *ServerHandler) TurboEOFAckTimeout() time.Duration {
	return durationFromMilliseconds(h.serverCfg.TurboEOFAckTimeoutMs, 2*time.Second)
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

// AggregateLinesChannelBufferSize returns the aggregate lines channel buffer size.
func (h *ServerHandler) AggregateLinesChannelBufferSize() int {
	return positiveIntOrDefault(h.serverCfg.ReadAggregateLineBufferSize, 10000)
}

// TurboDataTransmissionDelay returns the delay used after turbo flushes.
func (h *ServerHandler) TurboDataTransmissionDelay() time.Duration {
	return durationFromMilliseconds(h.serverCfg.TurboTransmissionDelayMs, 50*time.Millisecond)
}

// TurboEOFWaitDuration returns the wait duration used before signaling turbo EOF.
func (h *ServerHandler) TurboEOFWaitDuration(fileCount int) time.Duration {
	baseWait := durationFromMilliseconds(h.serverCfg.TurboEOFWaitBaseMs, 500*time.Millisecond)
	if fileCount <= 10 {
		return baseWait
	}

	perFileWait := durationFromMilliseconds(h.serverCfg.TurboEOFWaitPerFileMs, 10*time.Millisecond)
	maxWait := durationFromMilliseconds(h.serverCfg.TurboEOFWaitMaxMs, 2*time.Second)
	wait := time.Duration(fileCount) * perFileWait
	if wait > maxWait {
		return maxWait
	}
	return wait
}

// ShutdownTurboSerializeWait returns the wait before final turbo shutdown checks.
func (h *ServerHandler) ShutdownTurboSerializeWait() time.Duration {
	return durationFromMilliseconds(h.serverCfg.ShutdownTurboSerializeWaitMs, 500*time.Millisecond)
}

// ShutdownIdleRecheckWait returns the wait used for the final idle recheck.
func (h *ServerHandler) ShutdownIdleRecheckWait() time.Duration {
	return durationFromMilliseconds(h.serverCfg.ShutdownIdleRecheckWaitMs, 10*time.Millisecond)
}

func (h *ServerHandler) turboManagerConfig() turboManagerConfig {
	return turboManagerConfig{
		channelBufferSize: positiveIntOrDefault(h.serverCfg.TurboChannelBufferSize, defaultTurboChannelBufferSize),
		flushTimeout:      durationFromMilliseconds(h.serverCfg.TurboFlushTimeoutMs, defaultTurboFlushTimeout),
		flushPollInterval: durationFromMilliseconds(h.serverCfg.TurboFlushPollIntervalMs, defaultTurboFlushPollInterval),
		readRetryInterval: durationFromMilliseconds(h.serverCfg.TurboReadRetryIntervalMs, defaultTurboReadRetryInterval),
		eofAckQuietPeriod: durationFromMilliseconds(h.serverCfg.TurboTransmissionDelayMs, defaultTurboEOFAckQuietPeriod),
	}
}
