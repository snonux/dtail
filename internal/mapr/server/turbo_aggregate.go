package server

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/mapr/logformat"
)

// TurboAggregate is a high-performance aggregator for MapReduce operations in turbo mode.
// It processes lines directly without channels for maximum throughput.
type TurboAggregate struct {
	done *internal.Done
	// Hostname of the current server (used to populate $hostname field).
	hostname string
	// The mapr query
	query *mapr.Query
	// The mapr log format parser
	parser logformat.Parser
	// Group sets are swapped out during serialization to avoid clone-heavy flushes.
	groupMu   sync.Mutex
	groupSets map[string]*mapr.AggregateSet
	// serializeMu ensures only one serialization runs at a time.
	serializeMu sync.Mutex
	// Batch processing
	batchMu   sync.Mutex
	batch     []rawLine
	batchSize int
	// Periodic serialization
	serializeTicker *time.Ticker
	serialize       chan struct{}
	maprMessages    chan<- string
	// Stats
	linesProcessed atomic.Uint64
	errors         atomic.Uint64
	filesProcessed atomic.Uint64
	// Synchronization for clean shutdown.
	processorsWg sync.WaitGroup
	// Track active file processors
	activeProcessors atomic.Int32
	startOnce        sync.Once
	started          chan struct{}
}

type rawLine struct {
	content  *bytes.Buffer
	sourceID string
}

func (a *TurboAggregate) stopping() bool {
	select {
	case <-a.done.Done():
		return true
	default:
		return false
	}
}

func (a *TurboAggregate) stopSerializeTicker() {
	if a.serializeTicker != nil {
		a.serializeTicker.Stop()
	}
}

// NewTurboAggregate returns a new turbo mode aggregator.
func NewTurboAggregate(queryStr string, defaultLogFormat string) (*TurboAggregate, error) {
	query, err := mapr.NewQuery(queryStr)
	if err != nil {
		return nil, err
	}

	fqdn, err := config.Hostname()
	if err != nil {
		dlog.Server.Error(err)
	}
	s := strings.Split(fqdn, ".")

	parserName := resolveParserName(query, defaultLogFormat)

	dlog.Server.Info("Creating turbo log format parser",
		"parserName", parserName,
		"queryTable", query.Table,
		"queryLogFormat", query.LogFormat)
	logParser, err := logformat.NewParser(parserName, query)
	if err != nil {
		dlog.Server.Error("Could not create log format parser. Falling back to 'generic'", err)
		if logParser, err = logformat.NewParser("generic", query); err != nil {
			dlog.Server.FatalPanic("Could not create log format parser", err)
		}
	}

	return &TurboAggregate{
		done:      internal.NewDone(),
		serialize: make(chan struct{}, 1), // Buffered to avoid blocking
		hostname:  s[0],
		query:     query,
		parser:    logParser,
		groupSets: make(map[string]*mapr.AggregateSet),
		batchSize: 100, // Process 100 lines at a time
		batch:     make([]rawLine, 0, 100),
		started:   make(chan struct{}),
	}, nil
}

// countGroups returns the current number of groups in the aggregation.
func (a *TurboAggregate) countGroups() int {
	a.groupMu.Lock()
	defer a.groupMu.Unlock()
	return len(a.groupSets)
}

// Shutdown the aggregation engine.
func (a *TurboAggregate) Shutdown() {
	a.done.Shutdown()
	a.stopSerializeTicker()
	a.processorsWg.Wait()
	a.processBatchAndWait()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.doSerialize(ctx)
}

// Abort stops background processing without waiting for final serialization.
// Session generation replacement uses this to preempt old query work immediately.
func (a *TurboAggregate) Abort() {
	a.done.Shutdown()
	a.stopSerializeTicker()
}

// Start the turbo aggregation.
func (a *TurboAggregate) Start(ctx context.Context, maprMessages chan<- string) {
	a.maprMessages = maprMessages
	interval := a.query.Interval
	if interval <= 0 {
		interval = time.Second
	}
	a.serializeTicker = time.NewTicker(interval)
	a.startOnce.Do(func() {
		if a.started != nil {
			close(a.started)
		}
	})
	defer a.stopSerializeTicker()

	go a.serializationLoop(ctx)

	select {
	case <-ctx.Done():
	case <-a.done.Done():
	}
}

// ProcessLineDirect processes a line directly without channels.
// This is called from the TurboAggregateProcessor.
func (a *TurboAggregate) ProcessLineDirect(lineContent *bytes.Buffer, sourceID string) error {
	if a.stopping() {
		pool.RecycleBytesBuffer(lineContent)
		return nil
	}

	a.linesProcessed.Add(1)

	// Add to batch
	a.batchMu.Lock()
	a.batch = append(a.batch, rawLine{content: lineContent, sourceID: sourceID})
	shouldProcess := len(a.batch) >= a.batchSize
	a.batchMu.Unlock()

	if shouldProcess {
		a.processBatch()
	}

	return nil
}

// processBatch processes a full batch immediately.
func (a *TurboAggregate) processBatch() {
	a.processRawBatch(a.takeBatch())
}

// processBatchAndWait processes a batch of lines synchronously and waits for completion.
// This is used when flushing to ensure all data is processed before continuing.
func (a *TurboAggregate) processBatchAndWait() {
	a.processRawBatch(a.takeBatch())
}

func (a *TurboAggregate) takeBatch() []rawLine {
	a.batchMu.Lock()
	if len(a.batch) == 0 {
		a.batchMu.Unlock()
		return nil
	}
	batch := a.batch
	a.batch = make([]rawLine, 0, a.batchSize)
	a.batchMu.Unlock()
	return batch
}

func (a *TurboAggregate) processRawBatch(batch []rawLine) {
	for i := range batch {
		if err := a.processLine(batch[i].content, batch[i].sourceID); err != nil {
			a.errors.Add(1)
			dlog.Server.Error("Error processing line:", err, "lineIndex", i)
		}
		if batch[i].content != nil {
			pool.RecycleBytesBuffer(batch[i].content)
		}
	}
}

// processLine processes a single line and aggregates it.
func (a *TurboAggregate) processLine(lineContent *bytes.Buffer, sourceID string) error {
	maprLine := strings.TrimSpace(lineContent.String())
	parsedFields, err := a.parser.MakeFields(maprLine, sourceID)
	if err != nil {
		if err != logformat.ErrIgnoreFields {
			return err
		}
		return nil
	}

	// Apply where clause
	if !a.query.WhereClause(parsedFields) {
		return nil
	}

	// Apply set clause if needed
	if len(a.query.Set) > 0 {
		if err := a.query.SetClause(parsedFields); err != nil {
			return err
		}
	}

	// Aggregate the fields
	a.aggregate(parsedFields)
	return nil
}

// aggregate adds fields to the appropriate group.
func (a *TurboAggregate) aggregate(fields map[string]string) {
	groupKey := buildGroupKey(a.query.GroupBy, fields)
	a.groupMu.Lock()
	set, ok := a.groupSets[groupKey]
	if !ok {
		set = mapr.NewAggregateSet()
		a.groupSets[groupKey] = set
	}
	var addedSample bool
	for _, sc := range a.query.Select {
		if val, ok := fields[sc.Field]; ok {
			if err := set.Aggregate(sc.FieldStorage, sc.Operation, val, false); err != nil {
				dlog.Server.Error("TurboAggregate aggregation error", err, "field", sc.Field, "operation", sc.Operation)
				continue
			}
			addedSample = true
		}
	}
	if addedSample {
		set.Samples++
	}
	a.groupMu.Unlock()
}

// serializationLoop handles periodic serialization.
func (a *TurboAggregate) serializationLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.done.Done():
			return
		case <-a.serializeTicker.C:
			a.Serialize(ctx)
		case <-a.serialize:
			a.doSerialize(ctx)
		}
	}
}

// Serialize triggers serialization of all aggregated data.
func (a *TurboAggregate) Serialize(ctx context.Context) {
	select {
	case a.serialize <- struct{}{}:
	case <-time.After(time.Minute):
		dlog.Server.Warn("Starting to serialize mapreduce data takes over a minute")
	case <-ctx.Done():
	}
}

// doSerialize performs the actual serialization.
func (a *TurboAggregate) doSerialize(ctx context.Context) {
	a.serializeMu.Lock()
	defer a.serializeMu.Unlock()

	a.processBatchAndWait()
	if a.maprMessages == nil {
		dlog.Server.Error("TurboAggregate maprMessages channel is nil")
		return
	}

	snapshot := a.swapGroupSets()
	if len(snapshot) == 0 {
		return
	}

	group := mapr.NewGroupSet()
	for groupKey, aggregateSet := range snapshot {
		groupSet := group.GetSet(groupKey)
		*groupSet = *aggregateSet
	}

	serializeCtx := ctx
	if _, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		serializeCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	group.Serialize(serializeCtx, a.maprMessages)
}

func (a *TurboAggregate) swapGroupSets() map[string]*mapr.AggregateSet {
	a.groupMu.Lock()
	defer a.groupMu.Unlock()

	if len(a.groupSets) == 0 {
		return nil
	}

	snapshot := a.groupSets
	a.groupSets = make(map[string]*mapr.AggregateSet, len(snapshot))
	return snapshot
}

// TurboAggregateProcessor implements the line processor interface for turbo mode aggregation.
type TurboAggregateProcessor struct {
	aggregate *TurboAggregate
	globID    string
	flushOnce sync.Once
	closeOnce sync.Once
}

// NewTurboAggregateProcessor creates a new turbo aggregate processor.
func NewTurboAggregateProcessor(aggregate *TurboAggregate, globID string) *TurboAggregateProcessor {
	aggregate.processorsWg.Add(1)
	aggregate.activeProcessors.Add(1)
	return &TurboAggregateProcessor{
		aggregate: aggregate,
		globID:    globID,
	}
}

// ProcessLine processes a line directly to the turbo aggregate.
func (p *TurboAggregateProcessor) ProcessLine(lineContent *bytes.Buffer, _ uint64, sourceID string) error {
	if p.aggregate.stopping() {
		pool.RecycleBytesBuffer(lineContent)
		return nil
	}
	return p.aggregate.ProcessLineDirect(lineContent, sourceID)
}

// Flush ensures all buffered data is processed.
func (p *TurboAggregateProcessor) Flush() error {
	if p.aggregate.stopping() {
		return nil
	}

	p.flushOnce.Do(func() {
		p.aggregate.processBatchAndWait()
		p.aggregate.filesProcessed.Add(1)
	})
	return nil
}

// Close flushes any remaining data.
func (p *TurboAggregateProcessor) Close() error {
	err := p.Flush()
	p.closeOnce.Do(func() {
		p.aggregate.activeProcessors.Add(-1)
		p.aggregate.processorsWg.Done()
	})
	return err
}
