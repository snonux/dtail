package handlers

import (
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/mapr/server"
)

type readCommandServer interface {
	LogContext() interface{}
	SendServerMessage(message string)
	CanReadFile(path string) bool
	ServerMessagesChannel() chan string
	CatLimiter() chan struct{}
	TailLimiter() chan struct{}
	Hostname() string
	PlainOutput() bool
	Serverless() bool
	TurboBoostDisabled() bool
	HasRegularAggregate() bool
	RegisterAggregateLines(lines chan *line.Line)
	SharedLinesChannel() chan *line.Line
	TurboAggregate() *server.TurboAggregate
	AddPendingFiles(delta int32) int32
	CompletePendingFile() (remaining int32, activeCommands int32)
	PendingAndActive() (pending int32, activeCommands int32)
	TriggerShutdown()
	IsTurboMode() bool
	EnableTurboMode()
	HasTurboEOF() bool
	FlushTurboData()
	SignalTurboEOF()
	GetTurboChannel() chan []byte
	TurboChannelLen() int
	WaitForTurboEOFAck(timeout time.Duration) bool
	ReadGlobRetryInterval() time.Duration
	ReadRetryInterval() time.Duration
	AggregateLinesChannelBufferSize() int
	TurboDataTransmissionDelay() time.Duration
	TurboEOFWaitDuration(fileCount int) time.Duration
	ShutdownTurboSerializeWait() time.Duration
	ShutdownIdleRecheckWait() time.Duration
	TurboEOFAckTimeout() time.Duration
}

var _ readCommandServer = (*ServerHandler)(nil)

func (h *ServerHandler) LogContext() interface{} {
	return h.user
}

func (h *ServerHandler) SendServerMessage(message string) {
	h.sendln(h.serverMessages, message)
}

func (h *ServerHandler) CanReadFile(path string) bool {
	return h.user.HasFilePermission(path, "readfiles")
}

func (h *ServerHandler) ServerMessagesChannel() chan string {
	return h.serverMessages
}

func (h *ServerHandler) CatLimiter() chan struct{} {
	return h.catLimiter
}

func (h *ServerHandler) TailLimiter() chan struct{} {
	return h.tailLimiter
}

func (h *ServerHandler) Hostname() string {
	return h.hostname
}

func (h *ServerHandler) PlainOutput() bool {
	return h.plain
}

func (h *ServerHandler) Serverless() bool {
	return h.serverless
}

func (h *ServerHandler) TurboBoostDisabled() bool {
	return h.serverCfg.TurboBoostDisable
}

func (h *ServerHandler) HasRegularAggregate() bool {
	return h.aggregate != nil
}

func (h *ServerHandler) RegisterAggregateLines(lines chan *line.Line) {
	if h.aggregate != nil {
		h.aggregate.NextLinesCh <- lines
	}
}

func (h *ServerHandler) SharedLinesChannel() chan *line.Line {
	return h.lines
}

func (h *ServerHandler) TurboAggregate() *server.TurboAggregate {
	return h.turboAggregate
}

func (h *ServerHandler) AddPendingFiles(delta int32) int32 {
	return atomic.AddInt32(&h.pendingFiles, delta)
}

func (h *ServerHandler) CompletePendingFile() (remaining int32, activeCommands int32) {
	remaining = atomic.AddInt32(&h.pendingFiles, -1)
	activeCommands = atomic.LoadInt32(&h.activeCommands)
	return remaining, activeCommands
}

func (h *ServerHandler) PendingAndActive() (pending int32, activeCommands int32) {
	pending = atomic.LoadInt32(&h.pendingFiles)
	activeCommands = atomic.LoadInt32(&h.activeCommands)
	return pending, activeCommands
}

func (h *ServerHandler) TriggerShutdown() {
	h.shutdown()
}

func (h *ServerHandler) FlushTurboData() {
	h.flushTurboData()
}

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

func (h *ServerHandler) ReadGlobRetryInterval() time.Duration {
	return durationFromMilliseconds(h.serverCfg.ReadGlobRetryIntervalMs, 5*time.Second)
}

func (h *ServerHandler) ReadRetryInterval() time.Duration {
	return durationFromMilliseconds(h.serverCfg.ReadRetryIntervalMs, 2*time.Second)
}

func (h *ServerHandler) AggregateLinesChannelBufferSize() int {
	return positiveIntOrDefault(h.serverCfg.ReadAggregateLineBufferSize, 10000)
}

func (h *ServerHandler) TurboDataTransmissionDelay() time.Duration {
	return durationFromMilliseconds(h.serverCfg.TurboTransmissionDelayMs, 50*time.Millisecond)
}

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

func (h *ServerHandler) ShutdownTurboSerializeWait() time.Duration {
	return durationFromMilliseconds(h.serverCfg.ShutdownTurboSerializeWaitMs, 500*time.Millisecond)
}

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
