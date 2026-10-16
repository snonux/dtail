package pool

import (
	"bytes"
	"sync"
)

// BytesBuffer is there to optimize memory allocations. DTail otherwise allocates
// a lot of memory while reading logs.
var BytesBuffer = sync.Pool{
	New: func() any {
		b := bytes.Buffer{}
		// Increase initial capacity to 4KB to reduce reallocations
		// Most log lines are between 100-500 bytes, but some can be larger
		b.Grow(4096)
		return &b
	},
}

// MaxRecycledBytesBufferCap is the largest capacity of a buffer that
// RecycleBytesBuffer puts back into BytesBuffer. A buffer grew past it for an
// unusually long line (MaxLineLength may allow lines of several MiB); pooling
// it would keep that memory pinned for later lines that are mostly a few
// hundred bytes long, so it is left to the garbage collector instead.
const MaxRecycledBytesBufferCap = 1024 * 1024

// RecycleBytesBuffer resets the buffer and puts it back into BytesBuffer,
// unless its capacity is above MaxRecycledBytesBufferCap.
func RecycleBytesBuffer(b *bytes.Buffer) {
	if b.Cap() > MaxRecycledBytesBufferCap {
		return
	}
	b.Reset()
	BytesBuffer.Put(b)
}
