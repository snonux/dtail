package aggregate

import (
	"bytes"
	"sync"
	"unsafe"
)

const (
	scratchFieldsCapacity = 24
	scratchKeyCapacity    = 128
)

// lineScratch is the reusable working set of processLine: the field map handed
// to the log format parser and the buffer the group key is built in. Reusing
// them keeps the per-line path free of map, string and group-key allocations.
// Everything a scratch holds borrows the line buffer being processed, so a
// scratch is only valid while its own batch is being processed.
type lineScratch struct {
	fields map[string]string
	key    []byte
}

// lineScratchPool hands out one scratch per batch. The scratch cannot live on
// the Aggregate: several file processors call processRawBatch concurrently, and
// a shared fields map would let one file's line overwrite another's.
var lineScratchPool = sync.Pool{
	New: func() any {
		return &lineScratch{
			fields: make(map[string]string, scratchFieldsCapacity),
			key:    make([]byte, 0, scratchKeyCapacity),
		}
	},
}

// recycleLineScratch drops every borrowed view before the scratch is parked in
// the pool, so a pooled scratch can never hand a stale view of an already
// recycled line buffer to the next batch.
func recycleLineScratch(scratch *lineScratch) {
	clear(scratch.fields)
	scratch.key = scratch.key[:0]
	lineScratchPool.Put(scratch)
}

// borrowedLine returns the trimmed content of buf as a string sharing buf's
// memory instead of copying it, which is worth one string allocation per
// processed line. The result is only valid until buf is written to again or
// recycled into the buffer pool, which the caller does as soon as the line has
// been processed; see processLine for the copy-on-retention rules that make
// that safe.
func borrowedLine(buf *bytes.Buffer) string {
	if buf == nil {
		// bytes.Buffer.String renders a nil receiver as "<nil>". Keep that
		// legacy rendering — the parsers reject it like any other
		// non-mapreduce line — instead of dereferencing nil in Bytes.
		return buf.String()
	}
	return borrowedString(bytes.TrimSpace(buf.Bytes()))
}

// borrowedString reinterprets b as a string without copying it. The caller
// must guarantee that b is neither modified nor recycled while the returned
// string is still reachable.
func borrowedString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}
