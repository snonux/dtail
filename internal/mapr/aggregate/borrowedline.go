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
	// the idle storage summed over all line scratches of one pooled batch
	// scratch. A batch scratch holds up to processorBatchSize line scratches,
	// so the per-line limits alone would let it park 100 times as much storage
	// that nothing uses any more. Only idle headroom is charged: storage beyond
	// both the default size of a line scratch (scratchKeyCapacity,
	// scratchFieldsCapacity) and what the scratch's line in the batch just
	// processed used (its group-key length and field count; zero for a scratch
	// that batch did not use or whose line got no group key). Storage the last
	// batch used is never charged or shrunk: those lines were in memory
	// anyway, it is bounded by the per-line limits (at most processorBatchSize
	// times maxRetainedScratchKeyBytes and maxRetainedScratchFields, right
	// after a batch of nothing but maximal lines), and shrinking it would make
	// a steady workload of large lines regrow it on every batch. When the idle
	// total exceeds a budget, the scratches with the largest idle headroom are
	// shrunk to what their last line used (at least the default size) until
	// it fits. So after an outlier batch, the next ordinary batch finds the
	// outlier storage idle and trims it to the budget, and every later batch
	// of lines no larger than those reuses all scratches without allocating.
	// Only a workload whose line sizes keep changing by more than the budget
	// from one batch to the next still shrinks and regrows scratches. The
	// budget covers four line scratches of idle headroom at the per-line key
	// limit and eight at the per-line fields limit.
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
	// lastFields and lastKeyBytes are what the line this scratch held in the
	// batch just processed used: its field count and group-key length, zero
	// when that batch did not use the scratch. They are recorded when the
	// batch scratch is cleared, and the retention budget never charges or
	// shrinks storage up to them, see maxRetainedBatchScratchKeyBytes.
	lastFields   int
	lastKeyBytes int
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

// clear records what each line scratch used in the current batch, drops the
// borrowed views of the used ones, see clearLineScratch, enforces the batch
// retention budget and forgets the accepted lines.
func (b *batchScratch) clear() {
	for i, scratch := range b.lines {
		if i >= b.used {
			scratch.lastFields, scratch.lastKeyBytes = 0, 0
			continue
		}
		scratch.lastFields, scratch.lastKeyBytes = len(scratch.fields), len(scratch.key)
		clearLineScratch(scratch)
	}
	b.used = 0
	b.enforceRetentionBudget()
	clear(b.accepted)
	b.accepted = b.accepted[:0]
}

// enforceRetentionBudget caps the idle headroom that all line scratches of the
// batch scratch retain together at maxRetainedBatchScratchFields and
// maxRetainedBatchScratchKeyBytes, see there. It runs once per batch; the
// common case, a batch within budget, is a single walk over at most
// processorBatchSize scratches without any change.
func (b *batchScratch) enforceRetentionBudget() {
	var fields, keyBytes int
	for _, scratch := range b.lines {
		fields += idleFields(scratch)
		keyBytes += idleKeyBytes(scratch)
	}
	// Shrink the scratch with the largest idle headroom first, so that as few
	// scratches as possible lose storage. A shrunk scratch keeps what its last
	// line used, so its idle headroom drops to zero and the next line of the
	// same size fits without allocating. Each round removes one scratch's
	// headroom, so the loops end after at most len(b.lines) rounds, and only a
	// batch over budget runs them at all.
	for fields > maxRetainedBatchScratchFields {
		scratch := b.largest(idleFields)
		fields -= idleFields(scratch)
		scratch.fields = make(map[string]string, max(scratch.lastFields, scratchFieldsCapacity))
		scratch.maxFields = scratch.lastFields
	}
	for keyBytes > maxRetainedBatchScratchKeyBytes {
		scratch := b.largest(idleKeyBytes)
		keyBytes -= idleKeyBytes(scratch)
		scratch.key = make([]byte, 0, max(scratch.lastKeyBytes, scratchKeyCapacity))
	}
}

// largest returns the line scratch with the largest idle headroom. The caller
// only asks while the summed headroom is positive, so the result has some.
func (b *batchScratch) largest(idle func(*lineScratch) int) *lineScratch {
	var largest *lineScratch
	largestIdle := 0
	for _, scratch := range b.lines {
		if n := idle(scratch); n > largestIdle {
			largest, largestIdle = scratch, n
		}
	}
	return largest
}

// idleFields is how many entries' worth of map buckets scratch retains beyond
// both the default size of a line scratch's field map and the field count of
// its line in the batch just processed.
func idleFields(scratch *lineScratch) int {
	return max(scratch.maxFields-max(scratch.lastFields, scratchFieldsCapacity), 0)
}

// idleKeyBytes is how many bytes of group-key buffer scratch retains beyond
// both the default size of a line scratch's key buffer and the group-key
// length of its line in the batch just processed.
func idleKeyBytes(scratch *lineScratch) int {
	return max(cap(scratch.key)-max(scratch.lastKeyBytes, scratchKeyCapacity), 0)
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
