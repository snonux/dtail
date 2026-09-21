package aggregate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/mapr/logformat"
)

// reusingMapParser implements only logformat.Parser and clears and refills one
// map for every line, which Parser.MakeFields allows.
type reusingMapParser struct {
	fields map[string]string
}

func (p *reusingMapParser) MakeFields(maprLine, _ string) (map[string]string, error) {
	clear(p.fields)
	for _, token := range strings.Split(maprLine, "|") {
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			return nil, logformat.ErrIgnoreFields
		}
		p.fields[key] = value
	}
	return p.fields, nil
}

// panickingParser panics on its panicAt-th call (counting from 1).
type panickingParser struct {
	calls   int
	panicAt int
}

func (p *panickingParser) MakeFields(maprLine, _ string) (map[string]string, error) {
	p.calls++
	if p.calls == p.panicAt {
		panic("parser boom")
	}
	return map[string]string{"host": maprLine}, nil
}

func newAggregateWithParser(t *testing.T, queryText string,
	parser logformat.Parser) *Aggregate {

	t.Helper()
	query, err := mapr.NewQuery(queryText, logging.NopLogger{})
	if err != nil {
		t.Fatalf("NewQuery() error = %v", err)
	}
	aggregate, err := New(query, parser, "aggregate-test", logging.NopLogger{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return aggregate
}

// TestProcessorCopiesFieldsOfMapReusingParser pins that a batch's merge phase
// does not read a map the parser has since reused for a later line of the
// same batch. Without the copy every line of the batch is merged with the
// fields of the batch's last line: alpha would get last(color)=purple.
func TestProcessorCopiesFieldsOfMapReusingParser(t *testing.T) {
	parser := &reusingMapParser{fields: make(map[string]string)}
	aggregate := newAggregateWithParser(t,
		`from STATS select count(host),last(color) group by host`, parser)
	processor := NewProcessor(aggregate, "reuse")
	feedLines(t, processor,
		"host=alpha|color=orange",
		"host=alpha|color=violet",
		"host=beta|color=purple",
	)
	if err := processor.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	lastColor := aggregate.query.Select[1].FieldStorage
	aggregate.serializer.groupMu.Lock()
	defer aggregate.serializer.groupMu.Unlock()
	for group, want := range map[string]struct {
		samples int
		color   string
	}{
		"alpha": {2, "violet"},
		"beta":  {1, "purple"},
	} {
		set, ok := aggregate.serializer.groupSets[group]
		if !ok {
			t.Fatalf("group %q missing; groups: %v", group, aggregate.serializer.groupSets)
		}
		if set.Samples != want.samples {
			t.Errorf("group %q has %d samples, want %d", group, set.Samples, want.samples)
		}
		if got := set.SValues[lastColor]; got != want.color {
			t.Errorf("group %q last(color) = %q, want %q", group, got, want.color)
		}
	}
}

// TestProcessRawBatchKeepsParserPanicValue pins that a parser panic part way
// through a batch of several lines reaches the caller unchanged. Recycling the
// batch scratch must clear only the line scratches that exist, instead of
// slicing them by the batch length and replacing the panic with a runtime
// error of its own.
func TestProcessRawBatchKeepsParserPanicValue(t *testing.T) {
	// Leave a fresh batch scratch for this test's batch where the pool allows
	// it: a fresh one has fewer line scratches than the batch has lines, which
	// is the case that used to fail. A reused one keeps the test valid.
	for i := 0; i < 1000; i++ {
		scratch := batchScratchPool.Get().(*batchScratch)
		if len(scratch.lines) == 0 {
			batchScratchPool.Put(scratch)
			break
		}
	}

	parser := &panickingParser{panicAt: 4}
	aggregate := newAggregateWithParser(t,
		`from STATS select count(host) group by host`, parser)

	batch := make([]rawLine, 5)
	for i := range batch {
		batch[i] = rawLine{content: bytes.NewBufferString("alpha"), sourceID: "panic"}
	}
	recovered := func() (recovered any) {
		defer func() { recovered = recover() }()
		aggregate.processRawBatch(batch)
		return nil
	}()
	if recovered != "parser boom" {
		t.Fatalf("recovered %#v, want the parser's own panic value \"parser boom\"",
			recovered)
	}
}

// TestBatchScratchRecycleClearsOnlyUsedScratches covers the used-scratch
// bookkeeping deterministically with a fresh batch scratch.
func TestBatchScratchRecycleClearsOnlyUsedScratches(t *testing.T) {
	scratch := &batchScratch{}
	for i := 0; i < 3; i++ {
		line := scratch.line(i)
		line.fields["host"] = "alpha"
		line.key = append(line.key, "alpha"...)
	}
	if scratch.used != 3 {
		t.Fatalf("used = %d after three lines, want 3", scratch.used)
	}
	scratch.clear()
	if scratch.used != 0 {
		t.Errorf("used = %d after clear, want 0", scratch.used)
	}
	for i, line := range scratch.lines {
		if len(line.fields) != 0 || len(line.key) != 0 {
			t.Errorf("line scratch %d kept borrowed views after clear", i)
		}
	}
}

// TestBatchScratchRetentionBudget pins the batch-level retention limits: line
// scratches that each stay below the per-line limits must not add up to
// processorBatchSize times those limits in a pooled batch scratch.
func TestBatchScratchRetentionBudget(t *testing.T) {
	scratch := &batchScratch{}
	for i := 0; i < processorBatchSize; i++ {
		line := scratch.line(i)
		for f := 0; f < maxRetainedScratchFields; f++ {
			line.fields[string(rune('a'+f%26))+strings.Repeat("x", f/26)] = "v"
		}
		line.maxFields = len(line.fields)
		line.key = append(line.key, make([]byte, maxRetainedScratchKeyBytes)...)
	}
	scratch.clear()

	var fields, keyBytes int
	for _, line := range scratch.lines {
		fields += line.maxFields
		if cap(line.key) != scratchKeyCapacity {
			// Default-sized buffers are the uncharged floor.
			keyBytes += cap(line.key)
		}
	}
	if fields > maxRetainedBatchScratchFields {
		t.Errorf("batch scratch retains %d fields, want at most %d",
			fields, maxRetainedBatchScratchFields)
	}
	if keyBytes > maxRetainedBatchScratchKeyBytes {
		t.Errorf("batch scratch retains %d key bytes, want at most %d",
			keyBytes, maxRetainedBatchScratchKeyBytes)
	}
	// Scratches within the budget keep their storage for reuse.
	if scratch.lines[0].maxFields != maxRetainedScratchFields {
		t.Errorf("first line scratch dropped its in-budget map (maxFields = %d)",
			scratch.lines[0].maxFields)
	}

	// An ordinary full batch stays within the budget and is reused as is.
	ordinary := &batchScratch{}
	for i := 0; i < processorBatchSize; i++ {
		line := ordinary.line(i)
		for f := 0; f < 30; f++ {
			line.fields[string(rune('a'+f))] = "v"
		}
		line.maxFields = len(line.fields)
		line.key = append(line.key, "alpha"...)
	}
	ids := make([]uintptr, 0, processorBatchSize)
	for _, line := range ordinary.lines {
		ids = append(ids, mapIdentity(line.fields))
	}
	ordinary.clear()
	for i, line := range ordinary.lines {
		if mapIdentity(line.fields) != ids[i] {
			t.Fatalf("ordinary line scratch %d lost its map to the batch budget", i)
		}
	}
}
