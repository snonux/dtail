package server

import (
	"bytes"
	"context"
	"fmt"
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
	filesProcessed atomic.Uint64
	// Field map pool to reduce allocations
	fieldPool sync.Pool
	// Synchronization for clean shutdown
	processingWg sync.WaitGroup
	// Track active file processors
	activeProcessors atomic.Int32
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
		done:           internal.NewDone(),
		serialize:      make(chan struct{}, 1), // Buffered to avoid blocking
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

// countGroups returns the current number of groups in the aggregation.
func (a *TurboAggregate) countGroups() int {
	count := 0
	a.groupSets.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Shutdown the aggregation engine.
func (a *TurboAggregate) Shutdown() {
	dlog.Server.Info("TurboAggregate: Shutdown called", 
		"linesProcessed", a.linesProcessed.Load(),
		"filesProcessed", a.filesProcessed.Load(),
		"activeProcessors", a.activeProcessors.Load(),
		"currentGroups", a.countGroups())
	
	// Signal shutdown
	a.done.Shutdown()
	
	// Stop the ticker
	if a.serializeTicker != nil {
		a.serializeTicker.Stop()
	}
	
	// Wait for active processors to finish
	for a.activeProcessors.Load() > 0 {
		dlog.Server.Info("TurboAggregate: Waiting for active processors", 
			"activeProcessors", a.activeProcessors.Load())
		time.Sleep(10 * time.Millisecond)
	}
	
	// Process any remaining batch synchronously
	dlog.Server.Info("TurboAggregate: Processing final batch")
	a.processBatchAndWait()
	
	// Wait for all processing to complete
	dlog.Server.Info("TurboAggregate: Waiting for all processing to complete")
	a.processingWg.Wait()
	
	dlog.Server.Info("TurboAggregate: All processing complete, groups before final serialization", 
		"groupCount", a.countGroups())
	
	// Trigger final serialization after all processing is done
	// Use a longer timeout to ensure data gets through
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	dlog.Server.Info("TurboAggregate: Triggering final serialization")
	a.doSerialize(ctx)
	
	// Give more time for messages to be sent and processed
	// This is crucial to ensure the baseHandler's Read method picks up the messages
	dlog.Server.Info("TurboAggregate: Waiting for message delivery", 
		"channelLen", len(a.maprMessages))
	
	// Wait for channel to drain or timeout
	timeout := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	
	for {
		select {
		case <-timeout:
			dlog.Server.Warn("TurboAggregate: Timeout waiting for message delivery", 
				"remainingMessages", len(a.maprMessages))
			return
		case <-ticker.C:
			if len(a.maprMessages) == 0 {
				dlog.Server.Info("TurboAggregate: All messages delivered")
				return
			}
		}
	}
	
	dlog.Server.Info("TurboAggregate: Shutdown complete")
}

// Start the turbo aggregation.
func (a *TurboAggregate) Start(ctx context.Context, maprMessages chan<- string) {
	a.maprMessages = maprMessages
	
	dlog.Server.Info("TurboAggregate: Starting", 
		"interval", a.query.Interval)

	// Start periodic serialization
	a.serializeTicker = time.NewTicker(a.query.Interval)
	go a.serializationLoop(ctx)

	// Start batch processor
	go a.batchProcessorLoop(ctx)
	
	// Debug: Don't trigger immediate serialization - let data accumulate first
	dlog.Server.Info("TurboAggregate: Started, waiting for data")
}

// ProcessLineDirect processes a line directly without channels.
// This is called from the TurboAggregateProcessor.
func (a *TurboAggregate) ProcessLineDirect(lineContent []byte, sourceID string) error {
	// Increment counter first
	a.linesProcessed.Add(1)
	
	// Debug: Track when lines are received
	totalLines := a.linesProcessed.Load()
	if totalLines == 1 || totalLines%1000 == 0 {
		dlog.Server.Info("TurboAggregate: ProcessLineDirect called", 
			"totalLinesReceived", totalLines,
			"sourceID", sourceID,
			"lineLength", len(lineContent),
			"linePreview", string(lineContent[:min(50, len(lineContent))]))
	}
	
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
		dlog.Server.Info("TurboAggregate: First line received in batch", 
			"sourceID", sourceID,
			"batchSize", a.batchSize)
	}

	// Process batch if full
	if shouldProcess {
		dlog.Server.Debug("TurboAggregate: Batch full, processing", 
			"batchLen", batchLen)
		a.processBatch()
	}

	return nil
}

// batchProcessorLoop continuously processes batches.
func (a *TurboAggregate) batchProcessorLoop(ctx context.Context) {
	dlog.Server.Info("TurboAggregate: Batch processor loop started")
	defer dlog.Server.Info("TurboAggregate: Batch processor loop ended")
	
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-a.done.Done():
			dlog.Server.Info("TurboAggregate: Batch processor stopped by shutdown")
			// Process any remaining batch synchronously before exiting
			a.processBatchAndWait()
			return
		case <-ticker.C:
			// Periodically process any accumulated batch
			a.processBatch()
			
			// Check if context is done but only exit if no pending work
			select {
			case <-ctx.Done():
				a.batchMu.Lock()
				batchLen := len(a.batch)
				a.batchMu.Unlock()
				
				activeProcs := a.activeProcessors.Load()
				
				if batchLen > 0 || activeProcs > 0 {
					dlog.Server.Info("TurboAggregate: Context cancelled but work pending", 
						"batchLen", batchLen,
						"activeProcessors", activeProcs)
					// Continue processing
				} else {
					dlog.Server.Info("TurboAggregate: Context cancelled, no pending work")
					return
				}
			default:
				// Context not done, continue
			}
		}
	}
}

// processBatch processes a batch of lines asynchronously.
func (a *TurboAggregate) processBatch() {
	a.batchMu.Lock()
	if len(a.batch) == 0 {
		a.batchMu.Unlock()
		return
	}
	batch := a.batch
	batchSize := len(batch)
	a.batch = make([]rawLine, 0, a.batchSize)
	a.batchMu.Unlock()

	dlog.Server.Info("TurboAggregate: Processing batch", 
		"batchSize", batchSize,
		"totalLinesProcessed", a.linesProcessed.Load())

	// Track this batch processing
	a.processingWg.Add(1)
	defer a.processingWg.Done()

	// Process each line in the batch
	successCount := 0
	errorCount := 0
	for i, line := range batch {
		if err := a.processLine(line.content, line.sourceID); err != nil {
			a.errors.Add(1)
			errorCount++
			dlog.Server.Error("Error processing line:", err, "lineIndex", i)
		} else {
			successCount++
		}
		// Note: line count is already incremented in ProcessLineDirect
	}
	
	dlog.Server.Info("TurboAggregate: Batch processed", 
		"successCount", successCount,
		"errorCount", errorCount,
		"totalLinesProcessed", a.linesProcessed.Load())
}

// processBatchAndWait processes a batch of lines synchronously and waits for completion.
// This is used when flushing to ensure all data is processed before continuing.
func (a *TurboAggregate) processBatchAndWait() {
	a.batchMu.Lock()
	if len(a.batch) == 0 {
		a.batchMu.Unlock()
		return
	}
	batch := a.batch
	batchSize := len(batch)
	a.batch = make([]rawLine, 0, a.batchSize)
	a.batchMu.Unlock()

	dlog.Server.Info("TurboAggregate: Processing batch synchronously", 
		"batchSize", batchSize,
		"totalLinesProcessed", a.linesProcessed.Load())

	// Process each line in the batch (no goroutine, synchronous)
	successCount := 0
	errorCount := 0
	for i, line := range batch {
		if err := a.processLine(line.content, line.sourceID); err != nil {
			a.errors.Add(1)
			errorCount++
			dlog.Server.Error("Error processing line:", err, "lineIndex", i)
		} else {
			successCount++
		}
		// Note: line count is already incremented in ProcessLineDirect
	}
	
	dlog.Server.Info("TurboAggregate: Batch processed synchronously", 
		"successCount", successCount,
		"errorCount", errorCount,
		"totalLinesProcessed", a.linesProcessed.Load())
}

// processLine processes a single line and aggregates it.
func (a *TurboAggregate) processLine(lineContent []byte, sourceID string) error {
	// Trim whitespace
	maprLine := strings.TrimSpace(string(lineContent))

	// Debug: Log sample lines
	if a.linesProcessed.Load()%1000 == 0 {
		dlog.Server.Debug("TurboAggregate: Processing line", 
			"lineNumber", a.linesProcessed.Load(),
			"linePreview", maprLine[:min(100, len(maprLine))])
	}

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
			dlog.Server.Debug("TurboAggregate: Parser error", 
				"error", err,
				"line", maprLine[:min(100, len(maprLine))])
			return err
		}
		return nil
	}

	// Copy parsed fields to our pooled map
	for k, v := range parsedFields {
		fields[k] = v
	}
	
	// Debug: Log parsed fields for first few lines
	if a.linesProcessed.Load() < 5 {
		dlog.Server.Info("TurboAggregate: Parsed fields", 
			"lineNumber", a.linesProcessed.Load(),
			"fieldCount", len(fields),
			"fields", fields)
	}

	// Apply where clause
	if !a.query.WhereClause(fields) {
		dlog.Server.Debug("TurboAggregate: Line filtered by WHERE clause")
		return nil
	}

	// Apply set clause if needed
	if len(a.query.Set) > 0 {
		if err := a.query.SetClause(fields); err != nil {
			dlog.Server.Error("TurboAggregate: SET clause error", err)
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
		dlog.Server.Info("TurboAggregate: New group created", 
			"groupKey", groupKey,
			"totalGroups", a.countGroups())
	}

	// Aggregate the values
	var addedSample bool
	aggregatedFields := []string{}
	for _, sc := range a.query.Select {
		if val, ok := fields[sc.Field]; ok {
			if err := set.Aggregate(sc.FieldStorage, sc.Operation, val, false); err != nil {
				dlog.Server.Error("TurboAggregate: Aggregation error", 
					"field", sc.Field,
					"operation", sc.Operation,
					"error", err)
				continue
			}
			addedSample = true
			aggregatedFields = append(aggregatedFields, sc.Field)
		}
	}

	if addedSample {
		set.IncrementSamples()
		// Debug: Log aggregation details for first few samples
		if a.linesProcessed.Load() < 10 {
			dlog.Server.Info("TurboAggregate: Aggregated sample", 
				"groupKey", groupKey,
				"aggregatedFields", aggregatedFields,
				"sampleCount", set.GetSamples())
		}
	}
}

// serializationLoop handles periodic serialization.
func (a *TurboAggregate) serializationLoop(ctx context.Context) {
	dlog.Server.Info("TurboAggregate: Serialization loop started")
	defer dlog.Server.Info("TurboAggregate: Serialization loop ended")
	
	for {
		select {
		case <-ctx.Done():
			dlog.Server.Info("TurboAggregate: Serialization loop stopped by context")
			return
		case <-a.done.Done():
			dlog.Server.Info("TurboAggregate: Serialization loop stopped by shutdown")
			return
		case <-a.serializeTicker.C:
			dlog.Server.Info("TurboAggregate: Periodic serialization triggered")
			a.Serialize(ctx)
		case <-a.serialize:
			dlog.Server.Info("TurboAggregate: Manual serialization triggered")
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
	dlog.Server.Info("TurboAggregate: Starting serialization", 
		"linesProcessed", a.linesProcessed.Load(),
		"currentGroups", a.countGroups())
	
	// Process any remaining batch synchronously before serialization
	dlog.Server.Info("TurboAggregate: Processing remaining batch before serialization")
	a.processBatchAndWait()

	// Wait a moment for any in-progress batch processing
	dlog.Server.Info("TurboAggregate: Waiting for batch processing to complete")
	time.Sleep(50 * time.Millisecond) // Increased wait time

	// Lock to prevent concurrent modifications during serialization
	a.bufferMu.Lock()
	defer a.bufferMu.Unlock()

	// Count groups before serialization
	groupsBeforeSerialization := a.countGroups()
	dlog.Server.Info("TurboAggregate: Groups before serialization", 
		"count", groupsBeforeSerialization)
	
	if groupsBeforeSerialization == 0 {
		dlog.Server.Warn("TurboAggregate: No groups to serialize!")
		return
	}

	// Create a new group set for serialization
	group := mapr.NewGroupSet()

	// Copy all aggregate sets from the groupSets
	groupCount := 0
	sampleDetails := make([]string, 0)
	a.groupSets.Range(func(key, value interface{}) bool {
		groupKey := key.(string)
		safeSet := value.(*mapr.SafeAggregateSet)
		
		// Clone the safe set to get a regular AggregateSet
		clonedSet := safeSet.Clone()
		
		// Debug: Log details of first few groups
		if groupCount < 5 {
			sampleDetails = append(sampleDetails, 
				fmt.Sprintf("group=%s, samples=%d", groupKey, clonedSet.Samples))
		}
		
		// Add to the group set
		groupSet := group.GetSet(groupKey)
		*groupSet = *clonedSet
		groupCount++
		
		return true
	})

	dlog.Server.Info("TurboAggregate: Serialization details", 
		"groupCount", groupCount,
		"sampleGroups", sampleDetails,
		"maprMessagesChannel", a.maprMessages != nil)
	
	// Check if we have a valid channel
	if a.maprMessages == nil {
		dlog.Server.Error("TurboAggregate: maprMessages channel is nil!")
		return
	}
	
	// Serialize the group - use a longer timeout context for final serialization
	// to ensure data is sent even during shutdown
	serializeCtx := ctx
	if _, ok := ctx.Deadline(); ok {
		// If context has a deadline, extend it for serialization
		var cancel context.CancelFunc
		serializeCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	
	dlog.Server.Info("TurboAggregate: Calling group.Serialize", 
		"channelCap", cap(a.maprMessages),
		"channelLen", len(a.maprMessages))
	
	group.Serialize(serializeCtx, a.maprMessages)
	
	dlog.Server.Info("TurboAggregate: group.Serialize completed", 
		"sentGroups", groupCount,
		"channelLen", len(a.maprMessages))

	// Clear the groupSets after serialization only if not shutting down
	select {
	case <-a.done.Done():
		// During shutdown, keep the data for potential final serialization
		dlog.Server.Info("TurboAggregate: Keeping groupSets during shutdown")
	default:
		// Normal operation - clear for next interval
		dlog.Server.Info("TurboAggregate: Clearing groupSets for next interval")
		a.groupSets = sync.Map{}
	}
	
	// Log the state after serialization
	groupsAfterSerialization := a.countGroups()
	dlog.Server.Info("TurboAggregate: After serialization", 
		"groupsRemaining", groupsAfterSerialization)
}

// TurboAggregateProcessor implements the line processor interface for turbo mode aggregation.
type TurboAggregateProcessor struct {
	aggregate *TurboAggregate
	globID    string
}

// NewTurboAggregateProcessor creates a new turbo aggregate processor.
func NewTurboAggregateProcessor(aggregate *TurboAggregate, globID string) *TurboAggregateProcessor {
	aggregate.activeProcessors.Add(1)
	dlog.Server.Debug("TurboAggregate: New processor created", 
		"globID", globID,
		"activeProcessors", aggregate.activeProcessors.Load())
	return &TurboAggregateProcessor{
		aggregate: aggregate,
		globID:    globID,
	}
}

// ProcessLine processes a line directly to the turbo aggregate.
func (p *TurboAggregateProcessor) ProcessLine(lineContent *bytes.Buffer, lineNum uint64, sourceID string) error {
	// Debug: Log when ProcessLine is called
	if lineNum == 1 || lineNum%1000 == 0 {
		dlog.Server.Info("TurboAggregateProcessor: ProcessLine called",
			"lineNum", lineNum,
			"sourceID", sourceID,
			"contentLen", lineContent.Len())
	}
	
	// Process the line directly
	err := p.aggregate.ProcessLineDirect(lineContent.Bytes(), sourceID)
	
	// Recycle the buffer
	pool.RecycleBytesBuffer(lineContent)
	
	return err
}

// Flush ensures all buffered data is processed.
func (p *TurboAggregateProcessor) Flush() error {
	// Log flush call for debugging
	dlog.Server.Info("TurboAggregateProcessor: Flush called", 
		"globID", p.globID,
		"linesProcessed", p.aggregate.linesProcessed.Load())
	
	// Process any remaining batch synchronously
	p.aggregate.processBatchAndWait()
	
	// Increment files processed counter
	p.aggregate.filesProcessed.Add(1)
	
	dlog.Server.Info("TurboAggregateProcessor: Flush completed", 
		"globID", p.globID,
		"linesProcessed", p.aggregate.linesProcessed.Load(),
		"filesProcessed", p.aggregate.filesProcessed.Load())
	return nil
}

// Close flushes any remaining data.
func (p *TurboAggregateProcessor) Close() error {
	err := p.Flush()
	p.aggregate.activeProcessors.Add(-1)
	dlog.Server.Debug("TurboAggregate: Processor closed", 
		"globID", p.globID,
		"activeProcessors", p.aggregate.activeProcessors.Load())
	return err
}