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
	"github.com/mimecast/dtail/internal/protocol"
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
	// Group sets protected by mutex during serialization
	groupSets sync.Map    // map[string]*mapr.SafeAggregateSet
	bufferMu  sync.Mutex  // Protects serialization
	// Batch processing
	batchMu    sync.Mutex
	batch      []rawLine
	batchSize  int
	// Periodic serialization
	serializeTicker *time.Ticker
	serialize       chan struct{}
	maprMessages    chan<- string
	// Stats
	linesProcessed atomic.Uint64
	errors         atomic.Uint64
	// Field map pool to reduce allocations
	fieldPool sync.Pool
	// Synchronization for clean shutdown
	processingWg sync.WaitGroup
}

type rawLine struct {
	content  []byte
	sourceID string
}

// NewTurboAggregate returns a new turbo mode aggregator.
func NewTurboAggregate(queryStr string) (*TurboAggregate, error) {
	query, err := mapr.NewQuery(queryStr)
	if err != nil {
		return nil, err
	}

	fqdn, err := config.Hostname()
	if err != nil {
		dlog.Server.Error(err)
	}
	s := strings.Split(fqdn, ".")

	var parserName string
	switch query.LogFormat {
	case "":
		parserName = config.Server.MapreduceLogFormat
		if query.Table == "" {
			parserName = "generic"
		}
	default:
		parserName = query.LogFormat
	}

	dlog.Server.Info("Creating turbo log format parser", parserName)
	logParser, err := logformat.NewParser(parserName, query)
	if err != nil {
		dlog.Server.Error("Could not create log format parser. Falling back to 'generic'", err)
		if logParser, err = logformat.NewParser("generic", query); err != nil {
			dlog.Server.FatalPanic("Could not create log format parser", err)
		}
	}

	return &TurboAggregate{
		done:           internal.NewDone(),
		serialize:      make(chan struct{}),
		hostname:       s[0],
		query:          query,
		parser:         logParser,
		groupSets:      sync.Map{},
		batchSize:      100, // Process 100 lines at a time
		batch:          make([]rawLine, 0, 100),
		fieldPool: sync.Pool{
			New: func() interface{} {
				return make(map[string]string, 20)
			},
		},
	}, nil
}

// Shutdown the aggregation engine.
func (a *TurboAggregate) Shutdown() {
	dlog.Server.Info("Shutting down turbo aggregate", "linesProcessed", a.linesProcessed.Load())
	
	// Signal shutdown
	a.done.Shutdown()
	
	// Stop the ticker
	if a.serializeTicker != nil {
		a.serializeTicker.Stop()
	}
	
	// Process any remaining batch
	a.processBatch()
	
	// Wait for all processing to complete
	dlog.Server.Info("Waiting for all processing to complete")
	a.processingWg.Wait()
	
	// Trigger final serialization after all processing is done
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.doSerialize(ctx)
	
	// Give time for messages to be sent
	time.Sleep(100 * time.Millisecond)
}

// Start the turbo aggregation.
func (a *TurboAggregate) Start(ctx context.Context, maprMessages chan<- string) {
	a.maprMessages = maprMessages
	
	dlog.Server.Info("Starting turbo aggregate", "interval", a.query.Interval)

	// Start periodic serialization
	a.serializeTicker = time.NewTicker(a.query.Interval)
	go a.serializationLoop(ctx)

	// Start batch processor
	go a.batchProcessorLoop(ctx)
	
	// Also trigger an immediate serialization to ensure we capture data even if interval hasn't passed
	go func() {
		time.Sleep(50 * time.Millisecond) // Give time for some data to accumulate
		a.Serialize(ctx)
	}()
}

// ProcessLineDirect processes a line directly without channels.
// This is called from the TurboAggregateProcessor.
func (a *TurboAggregate) ProcessLineDirect(lineContent []byte, sourceID string) error {
	// Make a copy of the line content as the buffer will be recycled
	content := make([]byte, len(lineContent))
	copy(content, lineContent)

	// Add to batch
	a.batchMu.Lock()
	a.batch = append(a.batch, rawLine{content: content, sourceID: sourceID})
	shouldProcess := len(a.batch) >= a.batchSize
	batchLen := len(a.batch)
	a.batchMu.Unlock()

	if batchLen == 1 {
		dlog.Server.Debug("TurboAggregate: First line received", "sourceID", sourceID)
	}

	// Process batch if full
	if shouldProcess {
		a.processBatch()
	}

	return nil
}

// batchProcessorLoop continuously processes batches.
func (a *TurboAggregate) batchProcessorLoop(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Process any remaining batch before exiting
			a.processBatch()
			return
		case <-a.done.Done():
			// Process any remaining batch before exiting
			a.processBatch()
			return
		case <-ticker.C:
			// Periodically process any accumulated batch
			a.processBatch()
		}
	}
}

// processBatch processes a batch of lines.
func (a *TurboAggregate) processBatch() {
	a.batchMu.Lock()
	if len(a.batch) == 0 {
		a.batchMu.Unlock()
		return
	}
	batch := a.batch
	a.batch = make([]rawLine, 0, a.batchSize)
	a.batchMu.Unlock()

	// Track this batch processing
	a.processingWg.Add(1)
	defer a.processingWg.Done()

	// Process each line in the batch
	for _, line := range batch {
		if err := a.processLine(line.content, line.sourceID); err != nil {
			a.errors.Add(1)
			dlog.Server.Error("Error processing line:", err)
		}
		a.linesProcessed.Add(1)
	}
}

// processLine processes a single line and aggregates it.
func (a *TurboAggregate) processLine(lineContent []byte, sourceID string) error {
	// Trim whitespace
	maprLine := strings.TrimSpace(string(lineContent))

	// Get a field map from the pool
	fields := a.fieldPool.Get().(map[string]string)
	defer func() {
		// Clear the map before returning to pool
		for k := range fields {
			delete(fields, k)
		}
		a.fieldPool.Put(fields)
	}()

	// Parse the line
	parsedFields, err := a.parser.MakeFields(maprLine)
	if err != nil {
		if err != logformat.ErrIgnoreFields {
			return err
		}
		return nil
	}

	// Copy parsed fields to our pooled map
	for k, v := range parsedFields {
		fields[k] = v
	}

	// Apply where clause
	if !a.query.WhereClause(fields) {
		return nil
	}

	// Apply set clause if needed
	if len(a.query.Set) > 0 {
		if err := a.query.SetClause(fields); err != nil {
			return err
		}
	}

	// Aggregate the fields
	a.aggregate(fields)
	return nil
}

// aggregate adds fields to the appropriate group.
func (a *TurboAggregate) aggregate(fields map[string]string) {
	// Build group key
	var sb strings.Builder
	for i, field := range a.query.GroupBy {
		if i > 0 {
			sb.WriteString(protocol.AggregateGroupKeyCombinator)
		}
		if val, ok := fields[field]; ok {
			sb.WriteString(val)
		}
	}
	groupKey := sb.String()

	// Get or create the aggregate set
	setInterface, loaded := a.groupSets.LoadOrStore(groupKey, mapr.NewSafeAggregateSet())
	set := setInterface.(*mapr.SafeAggregateSet)
	
	if !loaded {
		dlog.Server.Debug("TurboAggregate: New group created", "groupKey", groupKey)
	}

	// Aggregate the values
	var addedSample bool
	for _, sc := range a.query.Select {
		if val, ok := fields[sc.Field]; ok {
			if err := set.Aggregate(sc.FieldStorage, sc.Operation, val, false); err != nil {
				dlog.Server.Error(err)
				continue
			}
			addedSample = true
		}
	}

	if addedSample {
		set.IncrementSamples()
	}
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
	// Process any remaining batch
	a.processBatch()

	// Wait a moment for any in-progress batch processing
	time.Sleep(10 * time.Millisecond)

	dlog.Server.Info("Serializing turbo mapreduce result", "linesProcessed", a.linesProcessed.Load())

	// Lock to prevent concurrent modifications during serialization
	a.bufferMu.Lock()
	defer a.bufferMu.Unlock()

	// Create a new group set for serialization
	group := mapr.NewGroupSet()

	// Copy all aggregate sets from the groupSets
	groupCount := 0
	a.groupSets.Range(func(key, value interface{}) bool {
		groupKey := key.(string)
		safeSet := value.(*mapr.SafeAggregateSet)
		
		// Clone the safe set to get a regular AggregateSet
		clonedSet := safeSet.Clone()
		
		// Add to the group set
		groupSet := group.GetSet(groupKey)
		*groupSet = *clonedSet
		groupCount++
		
		return true
	})

	dlog.Server.Info("Serializing groups", "groupCount", groupCount)
	
	// Serialize the group
	group.Serialize(ctx, a.maprMessages)

	// Clear the groupSets after serialization
	a.groupSets = sync.Map{}
}

// TurboAggregateProcessor implements the line processor interface for turbo mode aggregation.
type TurboAggregateProcessor struct {
	aggregate *TurboAggregate
	globID    string
}

// NewTurboAggregateProcessor creates a new turbo aggregate processor.
func NewTurboAggregateProcessor(aggregate *TurboAggregate, globID string) *TurboAggregateProcessor {
	return &TurboAggregateProcessor{
		aggregate: aggregate,
		globID:    globID,
	}
}

// ProcessLine processes a line directly to the turbo aggregate.
func (p *TurboAggregateProcessor) ProcessLine(lineContent *bytes.Buffer, lineNum uint64, sourceID string) error {
	// Process the line directly
	err := p.aggregate.ProcessLineDirect(lineContent.Bytes(), sourceID)
	
	// Recycle the buffer
	pool.RecycleBytesBuffer(lineContent)
	
	return err
}

// Flush ensures all buffered data is processed.
func (p *TurboAggregateProcessor) Flush() error {
	// Process any remaining batch
	p.aggregate.processBatch()
	return nil
}

// Close flushes any remaining data.
func (p *TurboAggregateProcessor) Close() error {
	return p.Flush()
}