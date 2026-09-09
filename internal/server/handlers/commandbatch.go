package handlers

import (
	"sync"
	"sync/atomic"

	maprserver "github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/omode"
)

// commandBatch coordinates completion of the initial in-process command
// stream. Read handlers are launched asynchronously, so the FIFO completion
// marker can arrive after every read was admitted but before those reads have
// registered their files in pendingFiles. Count one-shot read commands at
// admission time to close that gap.
type commandBatch struct {
	mu      sync.Mutex
	enabled bool
	open    atomic.Bool
	reads   map[*maprserver.Aggregate]int
}

type commandBatchRead struct {
	aggregate *maprserver.Aggregate
	tracked   bool
}

func (b *commandBatch) begin() {
	b.mu.Lock()
	b.enabled = true
	b.reads = make(map[*maprserver.Aggregate]int)
	b.open.Store(true)
	b.mu.Unlock()
}

func (b *commandBatch) beginRead(mode omode.Mode, aggregate *maprserver.Aggregate) commandBatchRead {
	if mode != omode.CatClient && mode != omode.GrepClient {
		return commandBatchRead{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	// The FIFO marker bounds ownership to the initial serverless command
	// stream. Interactive updates arriving later use the established legacy
	// coordinator and cannot be confused with an older batch generation.
	if !b.enabled || !b.open.Load() {
		return commandBatchRead{}
	}
	b.reads[aggregate]++
	return commandBatchRead{aggregate: aggregate, tracked: true}
}

// completeRead returns the exact aggregate whose admitted reads have drained.
// Capturing aggregate identity at admission prevents an old interactive
// generation from finishing a replacement aggregate after SESSION UPDATE.
func (b *commandBatch) completeRead(read commandBatchRead) *maprserver.Aggregate {
	if !read.tracked {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := b.reads[read.aggregate] - 1
	if remaining > 0 {
		b.reads[read.aggregate] = remaining
		return nil
	}
	b.reads[read.aggregate] = 0
	if b.open.Load() {
		return nil
	}

	delete(b.reads, read.aggregate)
	b.retireIfDrained()
	return read.aggregate
}

// complete closes admission and returns aggregates whose reads had already
// drained. Aggregates with active reads remain owned until completeRead.
func (b *commandBatch) complete() []*maprserver.Aggregate {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.open.Store(false)
	ready := make([]*maprserver.Aggregate, 0, len(b.reads))
	for aggregate, remaining := range b.reads {
		if remaining != 0 {
			continue
		}
		if aggregate != nil {
			ready = append(ready, aggregate)
		}
		delete(b.reads, aggregate)
	}
	b.retireIfDrained()
	return ready
}

// ownsAggregate reports whether this initial batch owns completion for the
// given aggregate. Later interactive generations are deliberately absent.
func (b *commandBatch) ownsAggregate(aggregate *maprserver.Aggregate) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled {
		return false
	}
	_, owned := b.reads[aggregate]
	return owned
}

func (b *commandBatch) retireIfDrained() {
	if !b.open.Load() && len(b.reads) == 0 {
		b.enabled = false
	}
}

func (b *commandBatch) isOpen() bool {
	return b.open.Load()
}
