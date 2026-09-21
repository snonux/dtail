package aggregate

import (
	"bytes"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/io/pool"
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
	parser := &panickingParser{panicAt: 4}
	aggregate := newAggregateWithParser(t,
		`from STATS select count(host) group by host`, parser)

	batch := make([]rawLine, 5)
	for i := range batch {
		batch[i] = rawLine{content: bytes.NewBufferString("alpha"), sourceID: "panic"}
	}
	recovered := func() (recovered any) {
		defer func() { recovered = recover() }()
		// A fresh batch scratch has fewer line scratches than the batch has
		// lines, which is the case that used to fail. A pooled one could
		// already hold a full batch of them and let the test pass vacuously.
		aggregate.processRawBatchWith(&batchScratch{}, batch)
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

// retainedExcess sums the storage beyond the default size that the line
// scratches of scratch retain, which is what the batch budget charges.
func retainedExcess(scratch *batchScratch) (fields, keyBytes int) {
	for _, line := range scratch.lines {
		fields += fieldsExcess(line)
		keyBytes += keyExcess(line)
	}
	return fields, keyBytes
}

// fillLineScratch simulates parseLine for one line with the given number of
// fields and a group key of keyLen bytes.
func fillLineScratch(line *lineScratch, fields, keyLen int) {
	for f := 0; f < fields; f++ {
		line.fields[fieldName(f)] = "v"
	}
	if n := len(line.fields); n > line.maxFields {
		line.maxFields = n
	}
	// One append of the whole key, as buildGroupKey does for a single group
	// field, so the buffer grows to the same capacity as in production.
	line.key = append(line.key[:0], keyBytes[:keyLen]...)
}

var keyBytes = make([]byte, maxRetainedScratchKeyBytes)

// fieldNames are precomputed so that filling a scratch in an allocation test
// does not allocate the names themselves.
var fieldNames = func() []string {
	names := make([]string, maxRetainedScratchFields)
	for f := range names {
		names[f] = string(rune('a'+f%26)) + strings.Repeat("x", f/26)
	}
	return names
}()

func fieldName(f int) string { return fieldNames[f] }

// TestBatchScratchRetentionBudget pins the batch-level retention limits: line
// scratches that each stay below the per-line limits must not add up to
// processorBatchSize times those limits in a pooled batch scratch. Only
// storage beyond the default size is charged, and the largest scratches are
// the ones shrunk.
func TestBatchScratchRetentionBudget(t *testing.T) {
	scratch := &batchScratch{}
	for i := 0; i < processorBatchSize; i++ {
		fillLineScratch(scratch.line(i), maxRetainedScratchFields, maxRetainedScratchKeyBytes)
	}
	scratch.clear()

	fields, keyBytes := retainedExcess(scratch)
	if fields > maxRetainedBatchScratchFields {
		t.Errorf("batch scratch retains %d fields beyond the default, want at most %d",
			fields, maxRetainedBatchScratchFields)
	}
	if keyBytes > maxRetainedBatchScratchKeyBytes {
		t.Errorf("batch scratch retains %d key bytes beyond the default, want at most %d",
			keyBytes, maxRetainedBatchScratchKeyBytes)
	}
	// The budget is not shrunk further than it has to be: it holds eight
	// scratches at the per-line fields limit and four at the key limit.
	perLineFields := maxRetainedScratchFields - scratchFieldsCapacity
	if want := maxRetainedBatchScratchFields / perLineFields * perLineFields; fields != want {
		t.Errorf("batch scratch retains %d fields beyond the default, want %d", fields, want)
	}
	for i, line := range scratch.lines {
		if fieldsExcess(line) == 0 && (len(line.fields) != 0 || line.maxFields != 0) {
			t.Errorf("shrunk line scratch %d kept fields state", i)
		}
		if keyExcess(line) == 0 && cap(line.key) != scratchKeyCapacity {
			t.Errorf("shrunk line scratch %d has key capacity %d, want %d",
				i, cap(line.key), scratchKeyCapacity)
		}
	}

	// Over budget, the largest scratches go first, wherever they sit: a
	// moderately oversized early scratch survives larger later ones.
	mixed := &batchScratch{}
	fillLineScratch(mixed.line(0), 200, 4096)
	for i := 1; i < processorBatchSize; i++ {
		fillLineScratch(mixed.line(i), maxRetainedScratchFields, maxRetainedScratchKeyBytes)
	}
	early := mixed.lines[0]
	earlyMap, earlyKey := mapIdentity(early.fields), sliceIdentity(early.key)
	mixed.clear()
	if mapIdentity(early.fields) != earlyMap || early.maxFields != 200 {
		t.Errorf("smaller early line scratch lost its map to larger later ones")
	}
	if sliceIdentity(early.key) != earlyKey {
		t.Errorf("smaller early line scratch lost its key buffer to larger later ones")
	}

	// An ordinary full batch stays within the budget and is reused as is.
	ordinary := &batchScratch{}
	for i := 0; i < processorBatchSize; i++ {
		fillLineScratch(ordinary.line(i), 30, 5)
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

// TestBatchScratchRecoversFromOutlierBatch is the regression test for a
// budget that charged default-sized storage and admitted scratches in order:
// after one outlier batch, the oversized early scratches used up the budget
// and every later default-sized scratch was replaced on every batch. After an
// outlier batch and at most one recovery batch, ordinary batches must not
// allocate, and what stays oversized must fit the budget.
func TestBatchScratchRecoversFromOutlierBatch(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector")
	}
	for _, tc := range []struct {
		name             string
		outliers         int
		outlierFields    int
		outlierKeyLength int
	}{
		// 4 group keys of about 60 KiB and 8 lines of 1000 fields each fit
		// the budget exactly as the reported cases; 20 outliers exceed it.
		{"keys", 4, 5, 60 * 1024},
		{"fields", 8, 1000, 5},
		{"keys over budget", 20, 5, 60 * 1024},
		{"fields over budget", 20, 1000, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scratch := &batchScratch{}
			batch := func(outliers int) {
				for i := 0; i < processorBatchSize; i++ {
					if i < outliers {
						fillLineScratch(scratch.line(i), tc.outlierFields, tc.outlierKeyLength)
						continue
					}
					fillLineScratch(scratch.line(i), 20, 64)
				}
				scratch.clear()
			}
			batch(tc.outliers)
			batch(0) // at most one recovery batch

			fields, keyBytes := retainedExcess(scratch)
			if fields > maxRetainedBatchScratchFields || keyBytes > maxRetainedBatchScratchKeyBytes {
				t.Errorf("batch scratch retains %d fields and %d key bytes beyond "+
					"the default, want at most %d and %d", fields, keyBytes,
					maxRetainedBatchScratchFields, maxRetainedBatchScratchKeyBytes)
			}
			if allocs := testing.AllocsPerRun(20, func() { batch(0) }); allocs != 0 {
				t.Errorf("an ordinary batch after an outlier batch made %.1f "+
					"allocations, want 0", allocs)
			}
		})
	}
}

// copyingParser implements only logformat.Parser, so the aggregate copies
// every field it returns into the line scratch's own map. Unlike the default
// parser, which parses only the fields a query uses, it therefore lets a line
// with very many fields inflate the scratch map. It does not allocate for
// lines whose field names it has seen before.
type copyingParser struct {
	fields map[string]string
}

func (p *copyingParser) MakeFields(maprLine, _ string) (map[string]string, error) {
	clear(p.fields)
	for rest := maprLine; rest != ""; {
		var token string
		token, rest, _ = strings.Cut(rest, "|")
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			return nil, logformat.ErrIgnoreFields
		}
		p.fields[key] = value
	}
	return p.fields, nil
}

// TestProcessorRecoversFromOutlierBatch runs the reported cases end to end:
// a batch with a few very long group keys (default parser), or a few lines
// with very many fields, must not make every later ordinary batch allocate.
func TestProcessorRecoversFromOutlierBatch(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector drops sync.Pool items at random, so pooled " +
			"line buffers and batch scratches are reallocated")
	}
	// One P keeps every batch on the same pooled batch scratch, so the
	// ordinary batches measured reuse the scratch the outlier batch inflated
	// instead of possibly a clean one parked on another P.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	var manyFields strings.Builder
	for f := 0; f < 1000; f++ {
		manyFields.WriteString("|f")
		manyFields.WriteString(strconv.Itoa(f))
		manyFields.WriteString("=v")
	}
	const query = `from STATS select count($line) group by color`
	for _, tc := range []struct {
		name     string
		parser   logformat.Parser
		outliers int
		outlier  string
		ordinary []string
	}{
		{
			name:     "keys",
			outliers: 4,
			outlier: "INFO|1002-071143|1|stats.go:56|8|15|7|0.21|471h0m21s|" +
				"MAPREDUCE:STATS|color=" + strings.Repeat("x", 60*1024),
			ordinary: retentionLines,
		},
		{
			name:     "fields",
			parser:   &copyingParser{fields: make(map[string]string)},
			outliers: 8,
			outlier:  "color=orange" + manyFields.String(),
			ordinary: []string{"host=alpha|color=orange|n=1", "host=beta|color=purple|n=2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var aggregate *Aggregate
			if tc.parser == nil {
				var err error
				aggregate, err = newAggregateFromTextForTest(query, logging.NopLogger{})
				if err != nil {
					t.Fatalf("Failed to create aggregate: %v", err)
				}
			} else {
				aggregate = newAggregateWithParser(t, query, tc.parser)
			}
			processor := NewProcessor(aggregate, "outlier")
			defer func() {
				if err := processor.Close(); err != nil {
					t.Errorf("Close() error = %v", err)
				}
			}()
			feedBatch := func(outliers int) {
				for i := 0; i < processorBatchSize; i++ {
					line := tc.ordinary[i%len(tc.ordinary)]
					if i < outliers {
						line = tc.outlier
					}
					buffer := pool.BytesBuffer.Get().(*bytes.Buffer)
					buffer.Reset()
					buffer.WriteString(line)
					if err := processor.ProcessLine(buffer, uint64(i+1), "outlier"); err != nil {
						t.Fatalf("ProcessLine() error = %v", err)
					}
				}
			}
			feedBatch(tc.outliers)
			feedBatch(0) // at most one recovery batch
			if allocs := testing.AllocsPerRun(20, func() { feedBatch(0) }); allocs != 0 {
				t.Errorf("an ordinary batch after an outlier batch made %.1f "+
					"allocations, want 0", allocs)
			}
		})
	}
}
