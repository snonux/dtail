package aggregate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/logging"
)

// The colors are all six bytes long so that overwriting a recycled buffer
// with a same-length line corrupts a retained view without changing any
// length, which is the subtlest form the bug could take.
var retentionLines = []string{
	"INFO|1002-071143|1|stats.go:56|8|15|7|0.21|471h0m21s|MAPREDUCE:STATS|host=alpha|color=orange",
	"INFO|1002-071144|1|stats.go:56|8|16|7|0.21|471h0m21s|MAPREDUCE:STATS|host=beta|color=purple",
	"INFO|1002-071145|1|stats.go:56|8|17|7|0.21|471h0m21s|MAPREDUCE:STATS|host=alpha|color=violet",
}

// TestAggregateDoesNotRetainRecycledLineBuffers is the guard for the central
// hazard of the allocation-free line path: the parsed fields, the group key
// and the line itself all borrow a pooled bytes.Buffer that the aggregator
// recycles as soon as the line has been processed. Everything the aggregation
// keeps — group keys, last() and len() strings — must therefore be a copy.
//
// The test feeds lines from pooled buffers, keeps hold of those buffers and
// overwrites them after the batch has been processed, which is exactly what
// the next reader taking them out of the pool does. Any retained view shows
// up as an 'X' in the aggregated state.
func TestAggregateDoesNotRetainRecycledLineBuffers(t *testing.T) {
	const query = `from STATS select count($line),last(color),len(color) group by host`

	aggregate, err := newAggregateFromTextForTest(query, logging.NopLogger{})
	if err != nil {
		t.Fatalf("Failed to create aggregate: %v", err)
	}

	processor := NewProcessor(aggregate, "retention")
	handed := make([]*bytes.Buffer, 0, len(retentionLines))
	for i, line := range retentionLines {
		buffer := pool.BytesBuffer.Get().(*bytes.Buffer)
		buffer.Reset()
		buffer.WriteString(line)
		handed = append(handed, buffer)
		if err := processor.ProcessLine(buffer, uint64(i+1), "retention"); err != nil {
			t.Fatalf("ProcessLine() error = %v", err)
		}
	}
	if err := processor.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if err := processor.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	for i, buffer := range handed {
		buffer.Reset()
		buffer.WriteString(strings.Repeat("X", len(retentionLines[i])))
	}

	wantLast := map[string]string{"alpha": "violet", "beta": "purple"}
	wantSamples := map[string]int{"alpha": 2, "beta": 1}

	aggregate.serializer.groupMu.Lock()
	defer aggregate.serializer.groupMu.Unlock()

	if len(aggregate.serializer.groupSets) != len(wantLast) {
		t.Fatalf("aggregated %d groups, want %d: %v", len(aggregate.serializer.groupSets),
			len(wantLast), aggregate.serializer.groupSets)
	}
	for groupKey, set := range aggregate.serializer.groupSets {
		if strings.ContainsRune(groupKey, 'X') {
			t.Errorf("group key %q is a view into a recycled line buffer", groupKey)
			continue
		}
		last, known := wantLast[groupKey]
		if !known {
			t.Errorf("unexpected group %q", groupKey)
			continue
		}
		if set.Samples != wantSamples[groupKey] {
			t.Errorf("group %q has %d samples, want %d", groupKey, set.Samples,
				wantSamples[groupKey])
		}
		lastFound := false
		for storage, value := range set.SValues {
			if strings.ContainsRune(value, 'X') {
				t.Errorf("group %q retained %s=%q, a view into a recycled line buffer",
					groupKey, storage, value)
			}
			if value == last {
				lastFound = true
			}
		}
		if !lastFound {
			t.Errorf("group %q SValues = %v, want %q among them", groupKey, set.SValues, last)
		}
		for storage, value := range set.FValues {
			if strings.HasPrefix(storage, "len(") && value != float64(len(last)) {
				t.Errorf("group %q has %s=%v, want %v", groupKey, storage, value,
					float64(len(last)))
			}
		}
	}
}

// TestProcessLineIsAllocationFree pins the point of this optimization: a line
// joining a group that already exists must not allocate at all. A regression
// (a per-line fields map, a copied line, a copied group key) shows up here as
// a nonzero allocation count.
func TestProcessLineIsAllocationFree(t *testing.T) {
	const query = `from STATS select count($line),sum($goroutines) group by host`

	aggregate, err := newAggregateFromTextForTest(query, logging.NopLogger{})
	if err != nil {
		t.Fatalf("Failed to create aggregate: %v", err)
	}

	scratch := lineScratchPool.Get().(*lineScratch)
	defer recycleLineScratch(scratch)

	buffer := pool.BytesBuffer.Get().(*bytes.Buffer)
	defer pool.RecycleBytesBuffer(buffer)
	buffer.Reset()
	buffer.WriteString(retentionLines[0])

	// Warm up: the first line of a group allocates the group itself.
	if err := aggregate.processLine(scratch, buffer, "alloc"); err != nil {
		t.Fatalf("processLine() error = %v", err)
	}

	allocs := testing.AllocsPerRun(100, func() {
		if err := aggregate.processLine(scratch, buffer, "alloc"); err != nil {
			t.Fatalf("processLine() error = %v", err)
		}
	})
	if allocs != 0 {
		t.Errorf("processLine() made %.1f allocations per line, want 0", allocs)
	}
}
