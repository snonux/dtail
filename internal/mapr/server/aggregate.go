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

// Aggregate is a high-performance aggregator for MapReduce operations.
// It processes lines directly without channels for maximum throughput.
type Aggregate struct {
	done *internal.Done
	// inputFinished is signaled via FinishInput once all one-shot input file
	// reads (cat/grep style) feeding this aggregate have drained. Start then
	// emits a final serialization and returns instead of blocking until
	// session teardown. Follow-mode (tail) inputs never signal it, so
	// interval-based streaming aggregation keeps running.
	inputFinished *internal.Done
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
	// Periodic serialization.
	// serializeTicker is published once by Start (before the serializationLoop
	// goroutine is launched) and read from two other places: serializationLoop's
	// select (ordered after the store by the go statement) and
	// stopSerializeTicker, which is reachable from Shutdown/Abort on the external
	// teardown goroutine with no other happens-before edge to Start's write. An
	// atomic.Pointer gives that publish-once/read-many access a lock-free
	// happens-before guarantee without coupling the ticker to serializeMu:
	// guarding it with serializeMu would make Abort's stop block behind an
	// in-flight doSerialize, violating Abort's immediate, non-blocking preemption
	// contract.
	serializeTicker atomic.Pointer[time.Ticker]
	serialize       chan struct{}
	// maprMessages is the output channel for serialized results. Start or its
	// caller publishes it through PrepareOutput, and doSerialize reads it while
	// holding the same mutex. This lets ServerHandler publish the destination
	// before exposing the aggregate to concurrent graceful shutdown.
	maprMessages chan<- string
	// Stats
	linesProcessed atomic.Uint64
	errors         atomic.Uint64
	filesProcessed atomic.Uint64
	// Synchronization for clean shutdown.
	processorsWg sync.WaitGroup
	processorMu  sync.Mutex
	// processorsSealed prevents Wait from racing a late processor registration.
	// Shutdown/Abort seals registration before waiting or returning; readers
	// that reach processor creation after cancellation receive an inert processor.
	processorsSealed bool
	// Track active file processors
	activeProcessors atomic.Int32
	startOnce        sync.Once
	started          chan struct{}
	shutdownOnce     sync.Once
	shutdownDone     chan struct{}
	terminalMu       sync.Mutex
	terminalState    aggregateTerminalState
	// finalizationCtx and finalizationCancelable are fixed when finalization
	// wins the terminal-state transition. Start may subsequently be the
	// goroutine that executes shutdownOnce, but it must use the graceful
	// caller's output context rather than silently replacing it with the
	// context-insensitive Shutdown behavior.
	finalizationCtx        context.Context
	finalizationCancelable bool
}

type aggregateTerminalState uint8

const (
	aggregateRunning aggregateTerminalState = iota
	aggregateFinalizing
	aggregateAborted
)

type rawLine struct {
	content  *bytes.Buffer
	sourceID string
}

func (a *Aggregate) stopping() bool {
	select {
	case <-a.done.Done():
		return true
	default:
		return false
	}
}

func (a *Aggregate) stopSerializeTicker() {
	// Load is safe from the external Shutdown/Abort teardown goroutine: Start
	// publishes the ticker with an atomic Store, so a nil load simply means
	// Start has not created it yet and there is nothing to stop.
	if ticker := a.serializeTicker.Load(); ticker != nil {
		ticker.Stop()
	}
}

// NewAggregate returns a new aggregator.
func NewAggregate(queryStr string, defaultLogFormat string) (*Aggregate, error) {
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

	dlog.Server.Info("Creating log format parser",
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

	return &Aggregate{
		done:          internal.NewDone(),
		inputFinished: internal.NewDone(),
		serialize:     make(chan struct{}, 1), // Buffered to avoid blocking
		hostname:      s[0],
		query:         query,
		parser:        logParser,
		groupSets:     make(map[string]*mapr.AggregateSet),
		batchSize:     100, // Process 100 lines at a time
		batch:         make([]rawLine, 0, 100),
		started:       make(chan struct{}),
		shutdownDone:  make(chan struct{}),
	}, nil
}

// countGroups returns the current number of groups in the aggregation.
func (a *Aggregate) countGroups() int {
	a.groupMu.Lock()
	defer a.groupMu.Unlock()
	return len(a.groupSets)
}

// Shutdown the aggregation engine.
func (a *Aggregate) Shutdown() {
	a.shutdown(nil)
}

// ShutdownContext finalizes the aggregation while output remains writable.
// Canceling ctx abandons blocked final sends promptly; the unsent snapshot is
// re-merged before shutdown completes so cancellation never corrupts state.
func (a *Aggregate) ShutdownContext(ctx context.Context) {
	a.shutdown(ctx)
}

func (a *Aggregate) shutdown(outputCtx context.Context) {
	finalizationCtx, finalizationCancelable, ok := a.claimFinalization(outputCtx)
	if !ok {
		return
	}
	a.shutdownOnce.Do(func() {
		defer close(a.shutdownDone)
		a.sealProcessors()
		a.done.Shutdown()
		a.stopSerializeTicker()
		a.processorsWg.Wait()
		a.processBatchAndWait()
		ctx, cancel := context.WithTimeout(finalizationCtx, 10*time.Second)
		defer cancel()
		if finalizationCancelable {
			a.doSerializeCancelable(ctx)
		} else {
			a.doSerialize(ctx)
		}
	})
	<-a.shutdownDone
}

// PrepareShutdown claims final serialization before the aggregate's command
// context is canceled. If Abort already owns termination, it has no effect.
// Claiming first keeps Start from returning and letting its caller close the
// result channel while a concurrent graceful shutdown serializes the last batch.
func (a *Aggregate) PrepareShutdown() {
	a.claimFinalization(nil)
}

// PrepareShutdownContext claims final serialization and binds the output
// lifetime that every shutdown participant must use. This must happen before
// canceling command work so Start cannot wake first and choose an
// context-insensitive final drain.
func (a *Aggregate) PrepareShutdownContext(ctx context.Context) {
	a.claimFinalization(ctx)
}

// Abort requests output-free termination and stops background processing
// without waiting. A finalization that already owns termination is allowed to
// finish; session generation replacement normally claims abort first and thus
// preempts old query work immediately.
func (a *Aggregate) Abort() {
	a.abort()
}

func (a *Aggregate) abort() bool {
	abortWon := a.claimAbort()
	a.sealProcessors()
	a.done.Shutdown()
	a.stopSerializeTicker()
	return abortWon
}

// AbortAndWait requests output-free termination and waits until every input
// processor has released its file and buffer resources.
func (a *Aggregate) AbortAndWait() {
	a.Abort()
	a.processorsWg.Wait()
}

// FinishInput signals that all one-shot input (cat/grep style file reads)
// feeding this aggregate has been fully consumed and no further processors
// will register. Start reacts by emitting a final serialization and
// returning, which lets a server-side map command complete once its input
// line channels are exhausted.
// Follow-mode (tail) inputs must never call this so that interval-based
// streaming aggregation keeps running. Safe to call multiple times.
func (a *Aggregate) FinishInput() {
	a.inputFinished.Shutdown()
}

// Start the aggregation. It blocks until the context is canceled, the
// aggregate is shut down, or — for one-shot inputs — FinishInput signals that
// all input has been consumed, in which case all remaining data is flushed
// and serialized before returning.
func (a *Aggregate) Start(ctx context.Context, maprMessages chan<- string) {
	// Publish the output channel under serializeMu. doSerialize reads
	// a.maprMessages while holding serializeMu (see line ~355), and it can be
	// invoked from a different goroutine than this one — Shutdown() is called
	// from the handler teardown path (baseHandler.Shutdown) concurrently with
	// this Start goroutine. Writing under the same lock the reader holds
	// establishes a happens-before edge, so the read is never torn or stale.
	// The internal serializationLoop reader is already ordered by the go
	// statement below, but the external Shutdown reader needs this lock.
	a.PrepareOutput(maprMessages)
	interval := a.query.Interval
	if interval <= 0 {
		interval = time.Second
	}
	// Publish the ticker before launching serializationLoop below. The store
	// happens-before that goroutine's Load via the go statement, and any later
	// stopSerializeTicker on the teardown goroutine observes it through the
	// atomic (see the serializeTicker field comment).
	a.serializeTicker.Store(time.NewTicker(interval))
	a.startOnce.Do(func() {
		if a.started != nil {
			close(a.started)
		}
	})
	defer a.stopSerializeTicker()

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		a.serializationLoop(ctx)
	}()

	shouldFinalize := false
	select {
	case <-ctx.Done():
		shouldFinalize = !a.abort()
	case <-a.done.Done():
		shouldFinalize = !a.abort()
	case <-a.inputFinished.Done():
		// All one-shot input is consumed: emit the final result and stop.
		// Shutdown waits for the processors, drains the batch and performs
		// the final serialization. Without this path a server-mode dmap
		// command would block here until session teardown while keeping the
		// session's active-command count nonzero — a circular wait that hung
		// the client forever even though all results had been transmitted.
		shouldFinalize = true
	}
	if shouldFinalize {
		// Shutdown owns the final processor join and serialization. Waiting here
		// keeps the producer channel valid until the final result has been sent,
		// including when command cancellation and external shutdown race.
		a.Shutdown()
	}

	// Stop the serialization loop and wait for it to exit before returning,
	// so no serialization can send on maprMessages once the caller closes its
	// side of the channel right after Start returns.
	a.done.Shutdown()
	<-loopDone
}

// PrepareOutput publishes the serialization destination before Start is
// scheduled. ServerHandler uses this barrier before exposing the aggregate to
// concurrent graceful shutdown; direct callers may continue to rely on Start
// publishing the same channel itself.
func (a *Aggregate) PrepareOutput(maprMessages chan<- string) {
	a.serializeMu.Lock()
	a.maprMessages = maprMessages
	a.serializeMu.Unlock()
}

// claimFinalization makes graceful final output the aggregate's terminal
// outcome. Abort and finalization race through this single state transition,
// so only one can win. Repeated Shutdown calls join the same finalization.
func (a *Aggregate) claimFinalization(outputCtx context.Context) (context.Context, bool, bool) {
	a.terminalMu.Lock()
	defer a.terminalMu.Unlock()

	switch a.terminalState {
	case aggregateRunning:
		a.terminalState = aggregateFinalizing
		a.finalizationCancelable = outputCtx != nil
		if outputCtx == nil {
			outputCtx = context.Background()
		}
		a.finalizationCtx = outputCtx
		return a.finalizationCtx, a.finalizationCancelable, true
	case aggregateFinalizing:
		return a.finalizationCtx, a.finalizationCancelable, true
	case aggregateAborted:
		return nil, false, false
	default:
		return nil, false, false
	}
}

// claimAbort makes abrupt, output-free termination the aggregate's terminal
// outcome. A finalization that already owns termination cannot be preempted.
func (a *Aggregate) claimAbort() bool {
	a.terminalMu.Lock()
	defer a.terminalMu.Unlock()

	switch a.terminalState {
	case aggregateRunning:
		a.terminalState = aggregateAborted
		return true
	case aggregateFinalizing:
		return false
	case aggregateAborted:
		return true
	default:
		return false
	}
}

// ProcessLineDirect processes a line directly without channels.
// This is called from the AggregateProcessor.
func (a *Aggregate) ProcessLineDirect(lineContent *bytes.Buffer, sourceID string) error {
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
func (a *Aggregate) processBatch() {
	a.processRawBatch(a.takeBatch())
}

// processBatchAndWait processes a batch of lines synchronously and waits for completion.
// This is used when flushing to ensure all data is processed before continuing.
func (a *Aggregate) processBatchAndWait() {
	a.processRawBatch(a.takeBatch())
}

func (a *Aggregate) takeBatch() []rawLine {
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

func (a *Aggregate) processRawBatch(batch []rawLine) {
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
func (a *Aggregate) processLine(lineContent *bytes.Buffer, sourceID string) error {
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

// aggregate adds fields to the appropriate group. The set is only created (or
// looked up) after at least one select field matches, preventing empty sets with
// Samples==0 from entering the map and causing 0/0 = NaN on the client for Avg.
func (a *Aggregate) aggregate(fields map[string]string) {
	groupKey := buildGroupKey(a.query.GroupBy, fields)
	a.groupMu.Lock()

	var set *mapr.AggregateSet
	var addedSample bool

	for _, sc := range a.query.Select {
		val, ok := fields[sc.Field]
		if !ok {
			continue
		}
		// Lazily look up or allocate the aggregate set on the first matching
		// field so that lines with no matching fields never create empty entries.
		if set == nil {
			set, ok = a.groupSets[groupKey]
			if !ok {
				set = mapr.NewAggregateSet()
				a.groupSets[groupKey] = set
			}
		}
		if err := set.Aggregate(sc.FieldStorage, sc.Operation, val, false); err != nil {
			dlog.Server.Error("Aggregate aggregation error", err, "field", sc.Field, "operation", sc.Operation)
			continue
		}
		addedSample = true
	}
	if addedSample {
		set.Samples++
	}
	a.groupMu.Unlock()
}

// serializationLoop handles periodic serialization.
func (a *Aggregate) serializationLoop(ctx context.Context) {
	// Start stores serializeTicker before launching this goroutine, so the load
	// is ordered-after that store and never nil here. The ticker pointer is
	// never replaced after Start, so loading it once is sufficient.
	ticker := a.serializeTicker.Load()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.done.Done():
			return
		case <-ticker.C:
			a.Serialize(ctx)
		case <-a.serialize:
			a.doSerialize(ctx)
		}
	}
}

// Serialize triggers serialization of all aggregated data.
func (a *Aggregate) Serialize(ctx context.Context) {
	select {
	case a.serialize <- struct{}{}:
	case <-time.After(time.Minute):
		dlog.Server.Warn("Starting to serialize mapreduce data takes over a minute")
	case <-ctx.Done():
	}
}

// doSerialize performs the actual serialization.
func (a *Aggregate) doSerialize(ctx context.Context) {
	serializeCtx := ctx
	if _, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		serializeCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	a.doSerializeCancelable(serializeCtx)
}

func (a *Aggregate) doSerializeCancelable(ctx context.Context) {
	a.serializeMu.Lock()
	defer a.serializeMu.Unlock()

	a.processBatchAndWait()
	if a.maprMessages == nil {
		dlog.Server.Error("Aggregate maprMessages channel is nil")
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

	remaining := group.Serialize(ctx, a.maprMessages)
	if len(remaining) > 0 {
		a.mergeRemainingLocked(remaining)
	}
}

// mergeRemainingLocked re-inserts aggregate sets that could not be sent during
// serialization back into the live groupSets map. Without this path the
// snapshot taken by swapGroupSets would be silently discarded on ctx
// cancellation and the next Serialize would not be able to retry the data.
//
// Concurrent ProcessLine calls may have already added new samples for the same
// group keys while the serialize was in flight. In that case we must preserve
// the newer live state for overwrite-style aggregations such as last() and
// len(), while still adding numeric contributions from the canceled snapshot.
func (a *Aggregate) mergeRemainingLocked(remaining map[string]*mapr.AggregateSet) {
	a.groupMu.Lock()
	defer a.groupMu.Unlock()
	for key, set := range remaining {
		existing, ok := a.groupSets[key]
		if !ok {
			a.groupSets[key] = set
			continue
		}
		mergeCancelledSnapshot(a.query, existing, set)
	}
	dlog.Server.Warn("Aggregate serialize interrupted; re-merged unsent groups",
		"remaining", len(remaining))
}

func mergeCancelledSnapshot(query *mapr.Query, live, snapshot *mapr.AggregateSet) {
	live.Samples += snapshot.Samples
	for _, sc := range query.Select {
		storage := sc.FieldStorage
		switch sc.Operation {
		case mapr.Count, mapr.Sum, mapr.Avg, mapr.Percentage, mapr.Percentile:
			live.FValues[storage] += snapshot.FValues[storage]
		case mapr.Min:
			liveValue, ok := live.FValues[storage]
			if !ok {
				live.FValues[storage] = snapshot.FValues[storage]
				continue
			}
			if snapshotValue := snapshot.FValues[storage]; snapshotValue < liveValue {
				live.FValues[storage] = snapshotValue
			}
		case mapr.Max:
			liveValue, ok := live.FValues[storage]
			if !ok {
				live.FValues[storage] = snapshot.FValues[storage]
				continue
			}
			if snapshotValue := snapshot.FValues[storage]; snapshotValue > liveValue {
				live.FValues[storage] = snapshotValue
			}
		case mapr.Last:
			if _, ok := live.SValues[storage]; !ok {
				if snapshotValue, ok := snapshot.SValues[storage]; ok {
					live.SValues[storage] = snapshotValue
				}
			}
		case mapr.Len:
			if _, ok := live.SValues[storage]; !ok {
				if snapshotValue, ok := snapshot.SValues[storage]; ok {
					live.SValues[storage] = snapshotValue
					live.FValues[storage] = snapshot.FValues[storage]
				}
			}
		default:
			dlog.Server.Error("Aggregate re-merge encountered unsupported aggregation",
				"operation", sc.Operation, "storage", storage)
		}
	}
}

func (a *Aggregate) swapGroupSets() map[string]*mapr.AggregateSet {
	a.groupMu.Lock()
	defer a.groupMu.Unlock()

	if len(a.groupSets) == 0 {
		return nil
	}

	snapshot := a.groupSets
	a.groupSets = make(map[string]*mapr.AggregateSet, len(snapshot))
	return snapshot
}

// AggregateProcessor implements the line processor interface for aggregation.
type AggregateProcessor struct {
	aggregate  *Aggregate
	globID     string
	registered bool
	flushOnce  sync.Once
	closeOnce  sync.Once
}

// NewAggregateProcessor creates a new aggregate processor.
func NewAggregateProcessor(aggregate *Aggregate, globID string) *AggregateProcessor {
	aggregate.processorMu.Lock()
	registered := !aggregate.processorsSealed
	if registered {
		aggregate.processorsWg.Add(1)
		aggregate.activeProcessors.Add(1)
	}
	aggregate.processorMu.Unlock()
	return &AggregateProcessor{
		aggregate:  aggregate,
		globID:     globID,
		registered: registered,
	}
}

// ProcessLine processes a line directly to the aggregate.
func (p *AggregateProcessor) ProcessLine(lineContent *bytes.Buffer, _ uint64, sourceID string) error {
	if !p.registered || p.aggregate.stopping() {
		pool.RecycleBytesBuffer(lineContent)
		return nil
	}
	return p.aggregate.ProcessLineDirect(lineContent, sourceID)
}

// Flush ensures all buffered data is processed.
func (p *AggregateProcessor) Flush() error {
	if !p.registered || p.aggregate.stopping() {
		return nil
	}

	p.flushOnce.Do(func() {
		p.aggregate.processBatchAndWait()
		p.aggregate.filesProcessed.Add(1)
	})
	return nil
}

// Close flushes any remaining data.
func (p *AggregateProcessor) Close() error {
	err := p.Flush()
	p.closeOnce.Do(func() {
		if p.registered {
			p.aggregate.activeProcessors.Add(-1)
			p.aggregate.processorsWg.Done()
		}
	})
	return err
}

func (a *Aggregate) sealProcessors() {
	a.processorMu.Lock()
	a.processorsSealed = true
	a.processorMu.Unlock()
}
