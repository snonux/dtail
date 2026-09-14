package aggregate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/mapr/logformat"
)

// Aggregate is a high-performance aggregator for MapReduce operations.
// It processes lines directly without channels for maximum throughput.
type Aggregate struct {
	logger logging.Logger
	done   *internal.Done
	// inputFinished is signaled via FinishInput once all one-shot input file
	// reads (cat/grep style) feeding this aggregate have drained. Start then
	// emits a final serialization and returns instead of blocking until
	// session teardown. Follow-mode (tail) inputs never signal it, so
	// interval-based streaming aggregation keeps running.
	inputFinished *internal.Done
	// The mapr query
	query *mapr.Query
	// The mapr log format parser
	parser logformat.Parser
	// Group sets are swapped out during serialization to avoid clone-heavy flushes.
	groupMu   sync.Mutex
	groupSets map[string]*mapr.AggregateSet
	// serializationPermit ensures only one serialization runs at a time while
	// still allowing a caller to abandon the wait when its context is canceled.
	serializationPermit chan struct{}
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
	// happens-before guarantee without coupling the ticker to the serialization
	// permit: sharing ownership would make Abort's stop block behind an
	// in-flight doSerialize, violating Abort's immediate, non-blocking preemption
	// contract.
	serializeTicker atomic.Pointer[time.Ticker]
	serialize       chan struct{}
	// maprMessages is the output channel for serialized results. Atomic
	// publication lets ServerHandler bind the destination before exposing the
	// aggregate to concurrent graceful shutdown without waiting behind an
	// in-flight serialization.
	maprMessages atomic.Pointer[aggregateOutput]
	// Stats
	linesProcessed atomic.Uint64
	errors         atomic.Uint64
	filesProcessed atomic.Uint64
	// Synchronization for clean shutdown.
	processorMu sync.Mutex
	// processorsSealed prevents completion waiting from racing a late processor
	// registration. Shutdown/Abort seals registration before waiting or
	// returning; readers that reach processor creation after cancellation receive
	// an inert processor.
	processorsSealed bool
	processorCount   int
	processorsDone   chan struct{}
	// Track active file processors
	activeProcessors atomic.Int32
	startOnce        sync.Once
	started          chan struct{}
	shutdownStarted  bool
	shutdownDone     chan struct{}
	terminalMu       sync.Mutex
	terminalState    aggregateTerminalState
	// finalizationCtx is fixed when finalization wins the terminal-state
	// transition. Start may subsequently be the goroutine that executes
	// finalization, but it must use the graceful caller's output context.
	finalizationCtx       context.Context
	finalizationOwnerHook func()
	serializationLoopHook func()
	serializationTickHook func()
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

type aggregateOutput struct {
	messages chan<- string
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

// New returns a new aggregator.
func New(queryStr string, defaultLogFormat string, logger logging.Logger) (*Aggregate, error) {
	return newAggregate(queryStr, defaultLogFormat, logformat.NewParser, logger)
}

// NewWithHostname returns a new aggregator using an injected process hostname.
func NewWithHostname(queryStr, defaultLogFormat, hostname string,
	logger logging.Logger) (*Aggregate, error) {
	return newAggregate(queryStr, defaultLogFormat,
		func(name string, query *mapr.Query) (logformat.Parser, error) {
			return logformat.NewParserWithHostname(name, query, hostname)
		}, logger)
}

func newAggregate(queryStr, defaultLogFormat string,
	newParser func(string, *mapr.Query) (logformat.Parser, error),
	logger logging.Logger) (*Aggregate, error) {
	logger = logging.OrNop(logger)
	query, err := mapr.NewQuery(queryStr, logger)
	if err != nil {
		return nil, err
	}

	parserName := resolveParserName(query, defaultLogFormat)

	logger.Info("Creating log format parser",
		"parserName", parserName,
		"queryTable", query.Table,
		"queryLogFormat", query.LogFormat)
	logParser, err := newParser(parserName, query)
	if err != nil {
		logger.Error("Could not create log format parser. Falling back to 'generic'", err)
		if logParser, err = newParser("generic", query); err != nil {
			return nil, fmt.Errorf("create fallback generic log format parser: %w", err)
		}
	}
	serializationPermit := make(chan struct{}, 1)
	serializationPermit <- struct{}{}

	return &Aggregate{
		logger:              logger,
		done:                internal.NewDone(),
		inputFinished:       internal.NewDone(),
		serialize:           make(chan struct{}, 1), // Buffered to avoid blocking
		serializationPermit: serializationPermit,
		query:               query,
		parser:              logParser,
		groupSets:           make(map[string]*mapr.AggregateSet),
		batchSize:           100, // Process 100 lines at a time
		batch:               make([]rawLine, 0, 100),
		started:             make(chan struct{}),
		shutdownDone:        make(chan struct{}),
	}, nil
}

// countGroups returns the current number of groups in the aggregation.
func (a *Aggregate) countGroups() int {
	a.groupMu.Lock()
	defer a.groupMu.Unlock()
	return len(a.groupSets)
}

// Shutdown finalizes the aggregation while output remains writable.
// Canceling ctx abandons blocked final sends promptly; the unsent snapshot is
// re-merged before shutdown completes so cancellation never corrupts state.
func (a *Aggregate) Shutdown(ctx context.Context) {
	a.shutdown(ctx, false)
}

// shutdown finalizes the aggregate and waits for either completion or the
// caller's context. The Start goroutine retains ownership of its output channel
// until finalization has completely stopped, even when its command context has
// already been canceled.
func (a *Aggregate) shutdown(ctx context.Context, retainOutput bool) {
	finalizationCtx, owner, ok := a.claimFinalization(ctx, true)
	if !ok {
		return
	}

	if owner {
		if a.finalizationOwnerHook != nil {
			a.finalizationOwnerHook()
		}
		func() {
			defer close(a.shutdownDone)
			processorsDone := a.sealProcessors()
			a.done.Shutdown()
			a.stopSerializeTicker()
			finalizeCtx, cancel := context.WithTimeout(finalizationCtx, 10*time.Second)
			defer cancel()
			select {
			case <-processorsDone:
			case <-finalizeCtx.Done():
				return
			}
			a.processBatchAndWait()
			a.doSerialize(finalizeCtx)
		}()
	}
	if retainOutput {
		// The finalizer observes finalizationCtx itself. The Start goroutine must
		// wait until that finalizer has completely stopped sending before its
		// caller can close the output channel.
		<-a.shutdownDone
		return
	}
	select {
	case <-a.shutdownDone:
	case <-ctx.Done():
	}
}

// PrepareShutdown claims final serialization and binds the output lifetime
// that every shutdown participant must use. This must happen before canceling
// command work so Start cannot wake first, choose an unrelated drain context,
// and let its caller close the result channel during final serialization. If
// Abort already owns termination, PrepareShutdown has no effect.
func (a *Aggregate) PrepareShutdown(ctx context.Context) {
	a.claimFinalization(ctx, false)
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
func (a *Aggregate) AbortAndWait(ctx context.Context) {
	if ctx == nil {
		panic("aggregate: nil abort context")
	}
	a.Abort()
	select {
	case <-a.processorsDoneChannel():
	case <-ctx.Done():
	}
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
	if ctx == nil {
		panic("aggregate: nil start context")
	}
	// Publish before launching the serialization loop. ServerHandler may have
	// already published this same destination before exposing the aggregate to
	// graceful shutdown; repeating the atomic store is harmless.
	a.PrepareOutput(ctx, maprMessages)
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
	loopPanic := make(chan any, 1)
	go func() {
		defer close(loopDone)
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("Recovered panic in aggregate serialization goroutine",
					"panic", recovered, "stack", string(debug.Stack()))
				a.abort()
				loopPanic <- recovered
			}
		}()
		a.serializationLoop(ctx)
	}()

	shouldFinalize := false
	var recovered any
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
	case recovered = <-loopPanic:
		a.abort()
	}
	if shouldFinalize {
		// Shutdown owns the final processor join and serialization. Waiting here
		// keeps the producer channel valid until the final result has been sent,
		// including when command cancellation and external shutdown race.
		a.shutdown(ctx, true)
	}

	// Stop the serialization loop and wait for it to exit before returning,
	// so no serialization can send on maprMessages once the caller closes its
	// side of the channel right after Start returns.
	a.done.Shutdown()
	<-loopDone
	if recovered == nil {
		select {
		case recovered = <-loopPanic:
		default:
		}
	}
	if recovered != nil {
		panic(recovered)
	}
}

// PrepareOutput publishes the serialization destination before Start is
// scheduled. ServerHandler uses this barrier before exposing the aggregate to
// concurrent graceful shutdown; direct callers may continue to rely on Start
// publishing the same channel itself.
func (a *Aggregate) PrepareOutput(ctx context.Context, maprMessages chan<- string) {
	if ctx == nil {
		panic("aggregate: nil output context")
	}
	if maprMessages == nil {
		a.maprMessages.Store(nil)
		return
	}
	a.maprMessages.Store(&aggregateOutput{messages: maprMessages})
}

// claimFinalization makes graceful final output the aggregate's terminal
// outcome. Abort and finalization race through this single state transition,
// so only one can win. Repeated Shutdown calls join the same finalization.
func (a *Aggregate) claimFinalization(outputCtx context.Context, start bool) (context.Context, bool, bool) {
	if outputCtx == nil {
		panic("aggregate: nil finalization context")
	}

	a.terminalMu.Lock()
	defer a.terminalMu.Unlock()

	switch a.terminalState {
	case aggregateRunning:
		a.terminalState = aggregateFinalizing
		a.finalizationCtx = outputCtx
	case aggregateFinalizing:
	case aggregateAborted:
		return nil, false, false
	default:
		return nil, false, false
	}
	owner := start && !a.shutdownStarted
	if owner {
		a.shutdownStarted = true
	}
	return a.finalizationCtx, owner, true
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
// This is called from the Processor.
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
			a.logger.Error("Error processing line:", err, "lineIndex", i)
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
		if !errors.Is(err, logformat.ErrIgnoreFields) {
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
			a.logger.Error("Aggregate aggregation error", err, "field", sc.Field, "operation", sc.Operation)
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
	if a.serializationLoopHook != nil {
		a.serializationLoopHook()
	}
	// Start stores serializeTicker before launching this goroutine, so the load
	// is ordered-after that store and never nil here. Tests also publish their
	// controlled ticker before launching the loop.
	ticker := a.serializeTicker.Load()
	for {
		// Prefer termination over work that was already ready when the loop
		// reached its select. Shutdown performs its own final serialization.
		select {
		case <-ctx.Done():
			return
		case <-a.done.Done():
			return
		default:
		}

		select {
		case <-ctx.Done():
			return
		case <-a.done.Done():
			return
		case <-ticker.C:
			if a.serializationTickHook != nil {
				a.serializationTickHook()
			}
			a.doSerialize(ctx)
		case <-a.serialize:
			a.doSerialize(ctx)
		}
	}
}

// Serialize requests serialization of all aggregated data. Requests coalesce
// while one is already pending; that pending pass will observe all data added
// before it acquires the aggregate locks.
func (a *Aggregate) Serialize(ctx context.Context) {
	if ctx == nil {
		panic("aggregate: nil serialize context")
	}
	select {
	case <-ctx.Done():
		return
	case <-a.done.Done():
		return
	default:
	}

	select {
	case a.serialize <- struct{}{}:
	case <-ctx.Done():
	case <-a.done.Done():
	default:
	}
}

// doSerialize performs the actual serialization.
func (a *Aggregate) doSerialize(ctx context.Context) {
	if ctx == nil {
		panic("aggregate: nil serialization context")
	}
	a.doSerializeCancelable(ctx)
}

func (a *Aggregate) doSerializeCancelable(ctx context.Context) {
	if !a.acquireSerialization(ctx) {
		return
	}
	defer a.releaseSerialization()

	a.processBatchAndWait()
	output := a.maprMessages.Load()
	if output == nil {
		a.logger.Error("Aggregate maprMessages channel is nil")
		return
	}

	snapshot := a.swapGroupSets()
	if len(snapshot) == 0 {
		return
	}

	group := mapr.NewGroupSet(a.logger)
	for groupKey, aggregateSet := range snapshot {
		groupSet := group.GetSet(groupKey)
		*groupSet = *aggregateSet
	}

	remaining := group.Serialize(ctx, output.messages)
	if len(remaining) > 0 {
		a.mergeRemainingLocked(remaining)
	}
}

func (a *Aggregate) acquireSerialization(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-a.serializationPermit:
		if ctx.Err() != nil {
			a.releaseSerialization()
			return false
		}
		return true
	}
}

func (a *Aggregate) releaseSerialization() {
	a.serializationPermit <- struct{}{}
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
		mergeCancelledSnapshot(a.query, existing, set, a.logger)
	}
	a.logger.Warn("Aggregate serialize interrupted; re-merged unsent groups",
		"remaining", len(remaining))
}

func mergeCancelledSnapshot(query *mapr.Query, live, snapshot *mapr.AggregateSet,
	logger logging.Logger) {
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
			logger.Error("Aggregate re-merge encountered unsupported aggregation",
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

// Processor implements the line processor interface for aggregation.
type Processor struct {
	aggregate  *Aggregate
	globID     string
	registered bool
	flushOnce  sync.Once
	closeOnce  sync.Once
}

// NewProcessor creates a new aggregate processor.
func NewProcessor(aggregate *Aggregate, globID string) *Processor {
	aggregate.processorMu.Lock()
	registered := !aggregate.processorsSealed
	if registered {
		aggregate.ensureProcessorsDoneLocked()
		aggregate.processorCount++
		aggregate.activeProcessors.Add(1)
	}
	aggregate.processorMu.Unlock()
	return &Processor{
		aggregate:  aggregate,
		globID:     globID,
		registered: registered,
	}
}

// ProcessLine processes a line directly to the aggregate.
func (p *Processor) ProcessLine(lineContent *bytes.Buffer, _ uint64, sourceID string) error {
	if !p.registered || p.aggregate.stopping() {
		pool.RecycleBytesBuffer(lineContent)
		return nil
	}
	return p.aggregate.ProcessLineDirect(lineContent, sourceID)
}

// Flush ensures all buffered data is processed.
func (p *Processor) Flush() error {
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
func (p *Processor) Close() error {
	var err error
	p.closeOnce.Do(func() {
		if p.registered {
			// Register accounting release before Flush. If batch processing
			// panics, aggregate abort/shutdown must still be able to join every
			// processor rather than waiting forever on a leaked reservation.
			defer p.aggregate.releaseProcessor()
			defer p.aggregate.activeProcessors.Add(-1)
		}
		err = p.Flush()
	})
	return err
}

func (a *Aggregate) sealProcessors() <-chan struct{} {
	a.processorMu.Lock()
	defer a.processorMu.Unlock()
	a.ensureProcessorsDoneLocked()
	if a.processorsSealed {
		return a.processorsDone
	}
	a.processorsSealed = true
	if a.processorCount == 0 {
		close(a.processorsDone)
	}
	return a.processorsDone
}

func (a *Aggregate) processorsDoneChannel() <-chan struct{} {
	return a.sealProcessors()
}

func (a *Aggregate) ensureProcessorsDoneLocked() {
	if a.processorsDone == nil {
		a.processorsDone = make(chan struct{})
	}
}

func (a *Aggregate) releaseProcessor() {
	a.processorMu.Lock()
	defer a.processorMu.Unlock()
	a.processorCount--
	if a.processorsSealed && a.processorCount == 0 {
		close(a.processorsDone)
	}
}
