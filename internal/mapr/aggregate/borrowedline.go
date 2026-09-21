package aggregate

import (
	"bytes"
	"sync"
	"unsafe"
)

const (
	scratchFieldsCapacity = 24
	scratchKeyCapacity    = 128
	// maxRetainedScratchKeyBytes and maxRetainedScratchFields bound what a
	// single line scratch keeps parked between batches. A single pathological
	// line -- a huge group key, or a query grouping by very many fields --
	// would otherwise inflate that scratch for as long as the pool holds it,
	// because neither a slice nor a map ever shrinks on its own. Both limits
	// are far above anything ordinary log lines reach, so the common case
	// still reuses its storage without allocating. Same policy as
	// maxRetainedLineBufBytes in internal/clients/handlers/basehandler.go.
	maxRetainedScratchKeyBytes = 64 * 1024
	maxRetainedScratchFields   = 1024
	// maxRetainedBatchScratchKeyBytes and maxRetainedBatchScratchFields bound
	// the same storage summed over all line scratches of one pooled batch
	// scratch. A batch scratch holds up to processorBatchSize line scratches,
	// so the per-line limits alone would let it park 100 times as much. The
	// budget still covers an ordinary batch with room to spare (about 80
	// fields and 2.5 KiB of group key per line of a full batch), so a
	// steady stream of ordinary lines never reallocates; only scratches
	// beyond the budget fall back to fresh, default-sized storage.
	maxRetainedBatchScratchKeyBytes = 256 * 1024
	maxRetainedBatchScratchFields   = 8 * 1024
)

// lineScratch is the reusable working set of parseLine: the field map the line
// is parsed into and the buffer the group key is built in. Reusing them keeps
// the per-line path free of map, string and group-key allocations. Everything
// a scratch holds borrows the line buffer being processed, so a scratch is
// only valid while its own batch is being processed.
type lineScratch struct {
	// fields holds the parsed fields of the line. A logformat.FieldsIntoParser
	// fills it directly; the map any other parser returns is copied into it,
	// because the merge phase reads it only after the whole batch was parsed
	// and a parser is free to reuse its own map on the next call.
	fields map[string]string
	key    []byte
	// maxFields is the high-water mark of len(fields) since fields was
	// allocated, i.e. how many entries' worth of buckets the map retains: a
	// map keeps its buckets after clear(), and the length at recycle time is
	// only the last line's, so the peak has to be recorded while the scratch
	// is in use for the retention limits to spot an inflated map.
	maxFields int
}

// batchScratch holds one lineScratch per line of a batch. processRawBatch
// parses every line of the batch into its own scratch first, without any lock,
// and only then takes the serializer's group lock once to merge the whole
// batch. The scratches of a batch therefore have to coexist, which is why one
// per line is needed instead of a single reused one.
type batchScratch struct {
	lines []*lineScratch
	// used is the number of line scratches handed out for the current batch.
	// It is tracked here rather than taken from the batch length so that
	// recycling after a panic part way through a batch clears exactly the
	// scratches that exist and were used, and the original panic survives.
	used int
	// accepted lists the scratches whose line passed the where clause and is
	// waiting to be merged into the serializer.
	accepted []*lineScratch
}

// batchScratchPool hands out one batch scratch per batch. The scratch cannot
// live on the Aggregate: several file processors call processRawBatch
// concurrently, and a shared fields map would let one file's line overwrite
// another's. It does not live on the Processor either, so that many idle
// follow-mode processors do not each pin a full batch worth of maps.
var batchScratchPool = sync.Pool{
	New: func() any { return &batchScratch{} },
}

func newLineScratch() *lineScratch {
	return &lineScratch{
		fields: make(map[string]string, scratchFieldsCapacity),
		key:    make([]byte, 0, scratchKeyCapacity),
	}
}

// line returns the scratch for the i-th line of the batch, growing the batch
// scratch on first use. Lines are handed out in order, so i is at most
// len(b.lines).
func (b *batchScratch) line(i int) *lineScratch {
	if i == len(b.lines) {
		b.lines = append(b.lines, newLineScratch())
	}
	if i >= b.used {
		b.used = i + 1
	}
	return b.lines[i]
}

// clear drops the borrowed views of the line scratches used by the current
// batch, see clearLineScratch, enforces the batch retention budget and forgets
// the accepted lines.
func (b *batchScratch) clear() {
	for _, scratch := range b.lines[:b.used] {
		clearLineScratch(scratch)
	}
	b.used = 0
	b.enforceRetentionBudget()
	clear(b.accepted)
	b.accepted = b.accepted[:0]
}

// enforceRetentionBudget caps the storage all line scratches of the batch
// scratch retain together at maxRetainedBatchScratchFields and
// maxRetainedBatchScratchKeyBytes. Scratches are admitted in order, and one
// that would exceed a budget gets fresh default-sized storage instead; that
// default storage (an empty map sized for scratchFieldsCapacity fields and a
// scratchKeyCapacity byte key buffer per line scratch) is the fixed floor and
// is not charged against the budget. It runs once per batch and walks at most
// processorBatchSize scratches.
func (b *batchScratch) enforceRetentionBudget() {
	var fields, keyBytes int
	for _, scratch := range b.lines {
		if fields+scratch.maxFields > maxRetainedBatchScratchFields {
			scratch.fields = make(map[string]string, scratchFieldsCapacity)
			scratch.maxFields = 0
		}
		fields += scratch.maxFields
		if keyBytes+cap(scratch.key) > maxRetainedBatchScratchKeyBytes {
			// The default-sized buffer is the floor every line scratch has
			// and is not charged against the budget.
			scratch.key = make([]byte, 0, scratchKeyCapacity)
			continue
		}
		keyBytes += cap(scratch.key)
	}
}

// recycleBatchScratch clears the used line scratches and parks the batch
// scratch in the pool. The scratch must not be touched afterwards.
func recycleBatchScratch(scratch *batchScratch) {
	scratch.clear()
	batchScratchPool.Put(scratch)
}

// clearLineScratch drops the borrowed views a scratch holds and releases
// storage that one outlier line inflated beyond the per-line retention limits,
// so the scratch is safe and reasonably sized to reuse. It runs before the
// owning batch scratch goes back to the pool, so a pooled scratch can never
// hand a stale view of an already recycled line buffer to the next batch.
func clearLineScratch(scratch *lineScratch) {
	if scratch.maxFields > maxRetainedScratchFields {
		// clear() keeps the buckets a huge line grew, so the map itself has
		// to go; the next line refills a right-sized one.
		scratch.fields = make(map[string]string, scratchFieldsCapacity)
		scratch.maxFields = 0
	} else {
		clear(scratch.fields)
	}

	if cap(scratch.key) > maxRetainedScratchKeyBytes {
		scratch.key = make([]byte, 0, scratchKeyCapacity)
	} else {
		scratch.key = scratch.key[:0]
	}
}

// borrowedLine returns the trimmed content of buf as a string sharing buf's
// memory instead of copying it, which is worth one string allocation per
// processed line. The result is only valid until buf is written to again or
// recycled into the buffer pool, which the caller does as soon as the line's
// batch has been processed; see parseLine for the copy-on-retention rules that
// make that safe.
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
