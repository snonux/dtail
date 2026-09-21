package readhub

import (
	"bytes"
	"os"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/io/pool"
)

// chunkSize is the size a chunk is published at at the latest; a follow
// reader publishes a chunk after every read anyway.
const chunkSize = 64 * 1024

type itemKind int

const (
	chunkItem itemKind = iota
	// restartItem: the file was truncated in place and is read again from its
	// beginning by the same read.
	restartItem
	// reopenItem: a new read of the file starts, e.g. after a rotation.
	reopenItem
	// longLineItem: the reader split a line that is longer than the maximum
	// line length; every session warns its client with its own file path.
	longLineItem
	// failedItem: the reader failed for good.
	failedItem
)

// item is one entry of a subscriber's queue.
type item struct {
	kind  itemKind
	chunk *chunk
	err   error
}

// chunk holds whole lines, as the follow reader fed them: without their
// newline. It is immutable once published and shared by every subscriber.
type chunk struct {
	data []byte
	// ends[i] is the end of line i in data; line i starts at ends[i-1].
	ends []int
	// offsets[i] is the byte offset in the file just past line i, or -1 when
	// the reader did not report it (see line.PositionObserver).
	offsets []int64
	// file identifies the file the offsets belong to; nil when unknown.
	file os.FileInfo
}

// line returns line i of the chunk.
func (c *chunk) line(i int) []byte {
	start := 0
	if i > 0 {
		start = c.ends[i-1]
	}
	// Cap the slice at the line's end: the chunk is shared by every session,
	// and an append must never reach into the next line.
	return c.data[start:c.ends[i]:c.ends[i]]
}

// lineEnd returns the position just past line i of the chunk.
func (c *chunk) lineEnd(i int) position {
	if c.file == nil || c.offsets[i] < 0 {
		return unknownPosition()
	}
	return position{offset: c.offsets[i], file: c.file}
}

// fanoutProcessor is the processor of an entry's reader. It packs the lines
// it is fed, and where each ends in the file, into chunks and publishes them,
// in order with the control items for truncation, to every subscriber. It
// runs on the reader's goroutine and is fed through a filter that passes
// every line, so each reported line end belongs to the line just added.
type fanoutProcessor struct {
	entry   *entry
	pending *chunk
}

var (
	_ line.Processor        = (*fanoutProcessor)(nil)
	_ line.RawProcessor     = (*fanoutProcessor)(nil)
	_ line.SourceRestarter  = (*fanoutProcessor)(nil)
	_ line.PositionObserver = (*fanoutProcessor)(nil)
)

func newFanoutProcessor(e *entry) *fanoutProcessor {
	return &fanoutProcessor{entry: e}
}

// ProcessRawLine adds a borrowed line to the pending chunk.
func (p *fanoutProcessor) ProcessRawLine(raw []byte, _ uint64, _ string) error {
	p.add(raw)
	return nil
}

// ProcessLine adds a line to the pending chunk and recycles its buffer. The
// reader uses it only if the raw path is unavailable.
func (p *fanoutProcessor) ProcessLine(lineBuf *bytes.Buffer, _ uint64, _ string) error {
	p.add(lineBuf.Bytes())
	pool.RecycleBytesBuffer(lineBuf)
	return nil
}

// LineEndsAt records where the line added last ends in the file.
func (p *fanoutProcessor) LineEndsAt(offset int64, file os.FileInfo) {
	if p.pending == nil || len(p.pending.offsets) == 0 {
		return
	}
	p.pending.offsets[len(p.pending.offsets)-1] = offset
	p.pending.file = file
}

// Flush publishes the pending chunk; the reader flushes after every read.
func (p *fanoutProcessor) Flush() error {
	p.publishWarning()
	p.publishPending()
	return nil
}

// Close does nothing: the entry's reader loop publishes what is pending.
func (p *fanoutProcessor) Close() error {
	return nil
}

// SourceRestarted publishes the lines of the old content, then the restart.
func (p *fanoutProcessor) SourceRestarted() {
	p.publishPending()
	p.entry.publish(item{kind: restartItem})
}

func (p *fanoutProcessor) add(raw []byte) {
	p.publishWarning()
	if p.pending != nil && len(p.pending.data)+len(raw) > cap(p.pending.data) {
		p.publishPending()
	}
	if p.pending == nil {
		p.pending = &chunk{data: make([]byte, 0, max(chunkSize, len(raw)))}
	}
	p.pending.data = append(p.pending.data, raw...)
	p.pending.ends = append(p.pending.ends, len(p.pending.data))
	p.pending.offsets = append(p.pending.offsets, -1)
}

// publishWarning publishes the long line warning the reader sent, if it
// did, after the lines fed before it. The reader sends the warning before it
// feeds the split line, through a channel with room for it, so the warning is
// always there when that line is added: every session gets it where a
// private reader would send it.
func (p *fanoutProcessor) publishWarning() {
	select {
	case <-p.entry.messages:
		p.publishPending()
		p.entry.publish(item{kind: longLineItem})
	default:
	}
}

// publishPending publishes the pending chunk, if there is one.
func (p *fanoutProcessor) publishPending() {
	if p.pending == nil {
		return
	}
	published := p.pending
	p.pending = nil
	p.entry.publish(item{kind: chunkItem, chunk: published})
}
