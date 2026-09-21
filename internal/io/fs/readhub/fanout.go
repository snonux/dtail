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
	// endOffset is the byte offset in the file just past the chunk's last
	// line, or -1 when unknown: for compressed files, and for a chunk that was
	// published because it was full rather than at the end of a read.
	endOffset int64
	// file identifies the file endOffset belongs to; nil when unknown.
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

// fanoutProcessor is the processor of an entry's reader. It packs the lines
// it is fed into chunks and publishes them, in order with the control items
// for truncation, to every subscriber. It runs on the reader's goroutine.
type fanoutProcessor struct {
	entry   *entry
	pending *chunk
	// endOffset and file are the position the reader reported for the lines
	// fed so far; they belong to the pending chunk when it is published next.
	endOffset int64
	file      os.FileInfo
}

var (
	_ line.Processor        = (*fanoutProcessor)(nil)
	_ line.RawProcessor     = (*fanoutProcessor)(nil)
	_ line.SourceRestarter  = (*fanoutProcessor)(nil)
	_ line.PositionObserver = (*fanoutProcessor)(nil)
)

func newFanoutProcessor(e *entry) *fanoutProcessor {
	return &fanoutProcessor{entry: e, endOffset: -1}
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

// LinesEndAt records where the lines fed so far end in the file.
func (p *fanoutProcessor) LinesEndAt(offset int64, file os.FileInfo) {
	p.endOffset = offset
	p.file = file
}

// Flush publishes the pending chunk; the reader flushes after every read.
func (p *fanoutProcessor) Flush() error {
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
	if p.pending != nil && len(p.pending.data)+len(raw) > cap(p.pending.data) {
		// The chunk is full in the middle of a read: its end is not a
		// position the reader reported.
		p.publish(-1, nil)
	}
	if p.pending == nil {
		p.pending = &chunk{data: make([]byte, 0, max(chunkSize, len(raw)))}
	}
	p.pending.data = append(p.pending.data, raw...)
	p.pending.ends = append(p.pending.ends, len(p.pending.data))
}

// publishPending publishes the pending chunk with the reported position.
func (p *fanoutProcessor) publishPending() {
	p.publish(p.endOffset, p.file)
}

func (p *fanoutProcessor) publish(endOffset int64, file os.FileInfo) {
	if p.pending == nil {
		return
	}
	published := p.pending
	p.pending = nil
	published.endOffset = endOffset
	published.file = file
	p.entry.publish(item{kind: chunkItem, chunk: published})
}
