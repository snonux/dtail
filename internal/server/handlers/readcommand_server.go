package handlers

import (
	"sync/atomic"

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
