package pool

import (
	"bytes"
	"testing"
)

// TestRecycleBytesBufferDropsLargeBuffers checks that a buffer grown above
// MaxRecycledBytesBufferCap is not put back into the pool, while a buffer of
// at most that capacity is reset for the pool. (Whether the pool then hands a
// buffer out again is up to sync.Pool, which drops some under the race
// detector, so only the dropped buffers are checked against a Get.)
func TestRecycleBytesBufferDropsLargeBuffers(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		pooled   bool
	}{
		{name: "default capacity", capacity: 4096, pooled: true},
		{name: "exactly the maximum", capacity: MaxRecycledBytesBufferCap, pooled: true},
		{name: "above the maximum", capacity: MaxRecycledBytesBufferCap + 1, pooled: false},
		{name: "several MiB", capacity: 8 * MaxRecycledBytesBufferCap, pooled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := bytes.NewBuffer(make([]byte, 0, tt.capacity))
			buf.WriteString("line")
			RecycleBytesBuffer(buf)
			if !tt.pooled {
				if got := BytesBuffer.Get().(*bytes.Buffer); got == buf {
					t.Fatalf("buffer of capacity %d was pooled", tt.capacity)
				}
				return
			}
			if buf.Len() != 0 {
				t.Fatalf("recycled buffer holds %d bytes, want it reset", buf.Len())
			}
		})
	}
}
