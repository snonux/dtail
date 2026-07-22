package server

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/source"
)

// ensureTestServerConfig initialises the minimum globals required by
// aggregate tests. Safe to call from multiple tests; it is idempotent.
func ensureTestServerConfig(t *testing.T) {
	t.Helper()
	if config.Common == nil {
		config.Common = &config.CommonConfig{
			Logger:   "none",
			LogLevel: "error",
		}
	}
	if config.Server == nil {
		config.Server = &config.ServerConfig{
			MapreduceLogFormat: "default",
		}
	}
	// dlog.Server.Error touches config.Client (TermColorsEnable) when it logs,
	// e.g. the nil-maprMessages branch in doSerialize. Provide a minimal client
	// config so those log calls do not nil-panic under test.
	if config.Client == nil {
		config.Client = &config.ClientConfig{TermColorsEnable: false}
	}
	if dlog.Server == nil {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		var wg sync.WaitGroup
		wg.Add(1)
		dlog.Start(ctx, &wg, source.Server)
	}
}

// TestAggregateDoSerializeReMergesOnCtxCancel verifies that when a
// serialize is cancelled after the live map has already advanced, the
// canceled snapshot is merged back without overwriting newer overwrite-style
// values. This guards against stale last()/len() values clobbering more recent
// updates that arrived after swapGroupSets.
func TestAggregateDoSerializeReMergesOnCtxCancel(t *testing.T) {
	ensureTestServerConfig(t)

	queryStr := `from STATS select count($time),last($message),len($message) from - group by $service`
	agg, err := NewAggregate(queryStr, config.Server.MapreduceLogFormat)
	if err != nil {
		t.Fatalf("NewAggregate failed: %v", err)
	}

	countStorage := agg.query.Select[0].FieldStorage
	lastStorage := agg.query.Select[1].FieldStorage
	lenStorage := agg.query.Select[2].FieldStorage

	agg.groupMu.Lock()
	agg.groupSets["svc"] = &mapr.AggregateSet{
		Samples: 1,
		FValues: map[string]float64{
			countStorage: 1,
			lenStorage:   float64(len("old-len")),
		},
		SValues: map[string]string{
			lastStorage: "old-last",
			lenStorage:  "old-len",
		},
	}
	agg.groupMu.Unlock()

	if got := agg.countGroups(); got != 1 {
		t.Fatalf("precondition: expected 1 group, got %d", got)
	}

	// Block the first send so doSerialize captures a snapshot and then waits
	// in AggregateSet.Serialize. While it is blocked we advance the live state
	// for the same group, then cancel the serialize context.
	messages := make(chan string)
	agg.maprMessages = messages

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		agg.doSerialize(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if got := agg.countGroups(); got == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for aggregate to swap live state")
		case <-time.After(5 * time.Millisecond):
		}
	}

	agg.groupMu.Lock()
	agg.groupSets["svc"] = &mapr.AggregateSet{
		Samples: 2,
		FValues: map[string]float64{
			countStorage: 2,
			lenStorage:   float64(len("new-len")),
		},
		SValues: map[string]string{
			lastStorage: "new-last",
			lenStorage:  "new-len",
		},
	}
	agg.groupMu.Unlock()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("doSerialize did not return after ctx cancel")
	}

	agg.groupMu.Lock()
	set, ok := agg.groupSets["svc"]
	agg.groupMu.Unlock()
	if !ok {
		t.Fatal("expected svc group to be re-merged after ctx cancel")
	}
	if got := set.Samples; got != 3 {
		t.Fatalf("expected merged samples to be 3, got %d", got)
	}
	if got := set.FValues[countStorage]; got != 3 {
		t.Fatalf("expected merged count to be 3, got %v", got)
	}
	if got := set.SValues[lastStorage]; got != "new-last" {
		t.Fatalf("expected latest last() value to survive cancel, got %q", got)
	}
	if got := set.SValues[lenStorage]; got != "new-len" {
		t.Fatalf("expected latest len() string value to survive cancel, got %q", got)
	}
	if got := set.FValues[lenStorage]; got != float64(len("new-len")) {
		t.Fatalf("expected latest len() numeric value to survive cancel, got %v", got)
	}
}

// TestAggregateProducesResults verifies the aggregate processes all
// input lines and produces serialized results. It was formerly a
// two-aggregator comparison that also exercised the regular channel-based
// server.Aggregate; that regular aggregate was deleted once this aggregate
// became the only aggregate path (task hv0), so only this subtest remains.
func TestAggregateProducesResults(t *testing.T) {
	// Initialize minimal config and logging
	if config.Common == nil {
		config.Common = &config.CommonConfig{
			Logger:   "none",
			LogLevel: "error",
		}
	}
	if config.Server == nil {
		config.Server = &config.ServerConfig{
			MapreduceLogFormat: "default",
		}
	}
	if dlog.Server == nil {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var wg sync.WaitGroup
		wg.Add(1)
		dlog.Start(ctx, &wg, source.Server)
	}

	// Test query
	queryStr := `from STATS select count($time),$time,avg($goroutines) from - group by $time order by $time`

	// Test data - DTail MapReduce format
	testLines := []string{
		"INFO|1002-071143|1|stats.go:56|8|15|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1",
		"INFO|1002-071143|1|stats.go:56|8|16|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1",
		"INFO|1002-071143|1|stats.go:56|8|17|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1",
		"INFO|1002-071147|1|stats.go:56|8|10|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1",
		"INFO|1002-071147|1|stats.go:56|8|11|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1",
	}

	t.Run("Aggregate", func(t *testing.T) {
		// Create aggregate
		agg, err := NewAggregate(queryStr, config.Server.MapreduceLogFormat)
		if err != nil {
			t.Fatalf("Failed to create aggregate: %v", err)
		}

		// Channel to collect messages
		messages := make(chan string, 100)
		// Use a cancellable context
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		startDone := make(chan struct{})
		go func() {
			defer close(startDone)
			agg.Start(ctx, messages)
		}()
		waitForAggregateStart(t, agg)

		// Process lines
		processor := NewAggregateProcessor(agg, "test")
		for i, line := range testLines {
			buf := bytes.NewBufferString(line)
			err := processor.ProcessLine(buf, uint64(i+1), "test")
			if err != nil {
				t.Errorf("Failed to process line %d: %v", i+1, err)
			}
		}

		// Flush to ensure all data is processed
		err = processor.Flush()
		if err != nil {
			t.Errorf("Failed to flush: %v", err)
		}

		// Close the processor to decrement activeProcessors
		err = processor.Close()
		if err != nil {
			t.Errorf("Failed to close processor: %v", err)
		}

		// Shutdown and get results
		agg.Shutdown()

		// Cancel context to stop background goroutines
		cancel()
		<-startDone

		// Collect results with timeout
		done := make(chan struct{})
		var results []string
		go func() {
			for msg := range messages {
				results = append(results, msg)
			}
			close(done)
		}()

		// Wait a bit for serialization
		time.Sleep(200 * time.Millisecond)
		close(messages)

		// Wait for collection to complete with timeout
		select {
		case <-done:
			// Good, collected all messages
		case <-time.After(2 * time.Second):
			t.Error("Timeout collecting messages")
		}

		t.Logf("Aggregate processed %d lines", agg.linesProcessed.Load())
		t.Logf("Aggregate results: %d messages", len(results))
		for _, r := range results {
			t.Logf("Result: %s", r)
		}

		// Verify we got results
		if len(results) == 0 {
			t.Error("Aggregate produced no results")
		}

		// Check line count
		if agg.linesProcessed.Load() != uint64(len(testLines)) {
			t.Errorf("Expected %d lines processed, got %d", len(testLines), agg.linesProcessed.Load())
		}
	})
}

// TestAggregateConcurrency tests aggregate with concurrent file processing
func TestAggregateConcurrency(t *testing.T) {
	// Initialize minimal config and logging
	if config.Common == nil {
		config.Common = &config.CommonConfig{
			Logger:   "none",
			LogLevel: "error",
		}
	}
	if config.Server == nil {
		config.Server = &config.ServerConfig{
			MapreduceLogFormat: "default",
		}
	}
	if dlog.Server == nil {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var wg sync.WaitGroup
		wg.Add(1)
		dlog.Start(ctx, &wg, source.Server)
	}

	queryStr := `from STATS select count($time),$time from - group by $time`

	// Create aggregate
	agg, err := NewAggregate(queryStr, config.Server.MapreduceLogFormat)
	if err != nil {
		t.Fatalf("Failed to create aggregate: %v", err)
	}

	// Channel to collect messages
	messages := make(chan string, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		agg.Start(ctx, messages)
	}()
	waitForAggregateStart(t, agg)

	// Process multiple "files" concurrently
	var wg sync.WaitGroup
	numFiles := 10
	linesPerFile := 100

	for f := 0; f < numFiles; f++ {
		wg.Add(1)
		go func(fileNum int) {
			defer wg.Done()

			processor := NewAggregateProcessor(agg, "file"+string(rune(fileNum)))

			// Process lines
			for i := 0; i < linesPerFile; i++ {
				line := "INFO|1002-071143|1|stats.go:56|8|15|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1"
				buf := bytes.NewBufferString(line)
				_ = processor.ProcessLine(buf, uint64(i+1), "file"+string(rune(fileNum)))
			}

			// Flush when file completes
			_ = processor.Flush()

			// Close the processor to decrement activeProcessors
			_ = processor.Close()
		}(f)
	}

	// Wait for all files to complete
	wg.Wait()

	// Shutdown and get results
	agg.Shutdown()
	cancel()
	<-startDone

	// Collect results
	time.Sleep(200 * time.Millisecond)
	close(messages)

	var results []string
	for msg := range messages {
		if strings.Contains(msg, "1002-071143") {
			results = append(results, msg)
		}
	}

	t.Logf("Processed %d lines total", agg.linesProcessed.Load())
	t.Logf("Processed %d files", agg.filesProcessed.Load())
	t.Logf("Got %d result messages", len(results))

	// Verify line count
	expectedLines := uint64(numFiles * linesPerFile)
	if agg.linesProcessed.Load() != expectedLines {
		t.Errorf("Expected %d lines processed, got %d", expectedLines, agg.linesProcessed.Load())
	}

	if agg.filesProcessed.Load() != uint64(numFiles) {
		t.Errorf("Expected %d files processed, got %d", numFiles, agg.filesProcessed.Load())
	}

	// Parse result to check count
	foundExpectedCount := false
	for _, result := range results {
		t.Logf("Result: %s", result)
		// The result should show count($time)≔1000 (10 files * 100 lines each)
		if strings.Contains(result, "count($time)≔1000") {
			t.Log("✓ Found expected count of 1000")
			foundExpectedCount = true
			break
		}
	}

	if !foundExpectedCount {
		t.Error("Did not find expected count of 1000 in results")
	}
}

func TestAggregateAbortReturnsPromptlyWithActiveProcessors(t *testing.T) {
	aggregate := &Aggregate{}
	aggregate.done = internal.NewDone()
	aggregate.activeProcessors.Store(1)

	done := make(chan struct{})
	go func() {
		aggregate.Abort()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Abort did not return promptly while processors were still active")
	}
}

func TestAggregateProcessorCountsFlushOnce(t *testing.T) {
	aggregate := &Aggregate{
		done:      internal.NewDone(),
		batchSize: 16,
	}

	processor := NewAggregateProcessor(aggregate, "test")
	if err := processor.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if err := processor.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if got := aggregate.filesProcessed.Load(); got != 1 {
		t.Fatalf("expected filesProcessed to be 1, got %d", got)
	}
	if got := aggregate.activeProcessors.Load(); got != 0 {
		t.Fatalf("expected activeProcessors to be 0, got %d", got)
	}
}

// TestAggregateFinishInputTerminatesStart is the regression test for the
// server-mode dmap deadlock: Start used to block until context cancel
// or session teardown even after all one-shot input had been consumed, which
// kept the server's map command active forever and hung the client after all
// results were delivered. With FinishInput, Start must emit the final
// serialization and return on its own.
func TestAggregateFinishInputTerminatesStart(t *testing.T) {
	ensureTestServerConfig(t)

	queryStr := `from STATS select count($time),$time from - group by $time`
	agg, err := NewAggregate(queryStr, config.Server.MapreduceLogFormat)
	if err != nil {
		t.Fatalf("NewAggregate failed: %v", err)
	}

	messages := make(chan string, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		agg.Start(ctx, messages)
	}()
	waitForAggregateStart(t, agg)

	processor := NewAggregateProcessor(agg, "test")
	testLines := []string{
		"INFO|1002-071143|1|stats.go:56|8|15|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1",
		"INFO|1002-071143|1|stats.go:56|8|16|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1",
		"INFO|1002-071147|1|stats.go:56|8|17|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1",
	}
	for i, lineStr := range testLines {
		if err := processor.ProcessLine(bytes.NewBufferString(lineStr), uint64(i+1), "test"); err != nil {
			t.Fatalf("ProcessLine failed: %v", err)
		}
	}
	if err := processor.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Signal input exhaustion; Start must finalize and return on its own,
	// without Shutdown or context cancellation.
	agg.FinishInput()

	select {
	case <-startDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after FinishInput (server-mode dmap deadlock)")
	}

	// After Start returned, no goroutine may send on messages anymore, so
	// closing and draining is race-free.
	close(messages)
	var results []string
	for msg := range messages {
		results = append(results, msg)
	}
	if len(results) == 0 {
		t.Fatal("expected a final serialized result after FinishInput")
	}
	foundCount := false
	for _, result := range results {
		if strings.Contains(result, "count($time)≔2") {
			foundCount = true
		}
	}
	if !foundCount {
		t.Fatalf("expected final result to contain count($time)≔2, got: %v", results)
	}
}

// TestAggregateStreamingContinuesWithoutFinishInput is the negative
// counterpart of the FinishInput regression test: a follow-mode (tail) map
// query never exhausts its input, so the aggregate must keep emitting
// interval-based interim results and Start must NOT return while the stream
// is live. This guards against over-eager finalization breaking continuous
// map queries over tailed logs.
func TestAggregateStreamingContinuesWithoutFinishInput(t *testing.T) {
	ensureTestServerConfig(t)

	queryStr := `from STATS select count($time),$time from - group by $time`
	agg, err := NewAggregate(queryStr, config.Server.MapreduceLogFormat)
	if err != nil {
		t.Fatalf("NewAggregate failed: %v", err)
	}
	// Fast serialization interval so the test observes interim results quickly.
	agg.query.Interval = 50 * time.Millisecond

	messages := make(chan string, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		agg.Start(ctx, messages)
	}()
	waitForAggregateStart(t, agg)

	// Keep the processor open for the whole test, simulating a followed file.
	processor := NewAggregateProcessor(agg, "test")
	feed := func(lineStr string) {
		t.Helper()
		if err := processor.ProcessLine(bytes.NewBufferString(lineStr), 1, "test"); err != nil {
			t.Fatalf("ProcessLine failed: %v", err)
		}
	}
	waitForResult := func(what string) string {
		t.Helper()
		select {
		case msg := <-messages:
			return msg
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s interval result", what)
			return ""
		}
	}

	feed("INFO|1002-071143|1|stats.go:56|8|15|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1")
	first := waitForResult("first")

	feed("INFO|1002-071147|1|stats.go:56|8|16|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1")
	second := waitForResult("second")

	if first == "" || second == "" {
		t.Fatal("expected two non-empty interval results")
	}

	// The stream is still live: Start must not have returned.
	select {
	case <-startDone:
		t.Fatal("Start returned although the follow-mode input never signaled FinishInput")
	default:
	}

	// Cleanup: close the processor before Shutdown (Shutdown waits for all
	// processors), then wait for Start to return.
	if err := processor.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	agg.Shutdown()
	select {
	case <-startDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Shutdown")
	}
}

// TestAggregateStartDoSerializeFieldRace exercises the concurrent access to
// the maprMessages field. Start publishes a.maprMessages while a separate
// goroutine runs doSerialize — the read site (aggregate.go ~355) reached
// in production via baseHandler.Shutdown -> Aggregate.Shutdown ->
// doSerialize, which runs on a different goroutine than the one executing Start.
// Before the fix the write in Start was unsynchronized while doSerialize read
// the field under serializeMu: a data race under the Go memory model even though
// the nil check prevented a crash. Start now publishes the field under
// serializeMu (the same lock doSerialize holds), establishing happens-before, so
// -race must stay clean across many tight iterations.
func TestAggregateStartDoSerializeFieldRace(t *testing.T) {
	ensureTestServerConfig(t)

	queryStr := `from STATS select count($time),$time from - group by $time`
	const iterations = 500

	for i := 0; i < iterations; i++ {
		agg, err := NewAggregate(queryStr, config.Server.MapreduceLogFormat)
		if err != nil {
			t.Fatalf("NewAggregate failed: %v", err)
		}

		messages := make(chan string, 8)
		ctx, cancel := context.WithCancel(context.Background())

		// Release both goroutines as close together as possible so the write to
		// a.maprMessages at the top of Start overlaps the read inside
		// doSerialize. No lines are fed, so doSerialize takes the empty-snapshot
		// path and never sends on messages.
		release := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-release
			agg.Start(ctx, messages)
		}()
		go func() {
			defer wg.Done()
			<-release
			agg.doSerialize(ctx)
		}()
		close(release)

		// doSerialize returns quickly; cancel so Start unblocks its select and
		// its serialization loop exits before the next iteration.
		cancel()
		wg.Wait()

		close(messages)
		for range messages { //nolint:revive // drain any (unexpected) output
		}
	}
}

// TestAggregateStartStopTickerFieldRace exercises the concurrent access to
// the serializeTicker field. Start creates and publishes a.serializeTicker while
// a separate goroutine runs Abort -> stopSerializeTicker, which reads the field.
// In production stopSerializeTicker is reached from baseHandler.Shutdown ->
// Aggregate.Shutdown/Abort on the teardown goroutine, a different goroutine
// than the one executing Start. Before the fix the write in Start was a plain
// unsynchronized pointer store while stopSerializeTicker read the pointer with no
// happens-before edge: a data race under the Go memory model even though the nil
// check prevented a crash. Start now publishes the ticker with an atomic Store
// and stopSerializeTicker reads it with an atomic Load, so -race must stay clean
// across many tight iterations. This test deliberately omits
// waitForAggregateStart so the ticker write and read can actually overlap.
func TestAggregateStartStopTickerFieldRace(t *testing.T) {
	ensureTestServerConfig(t)

	queryStr := `from STATS select count($time),$time from - group by $time`
	const iterations = 500

	for i := 0; i < iterations; i++ {
		agg, err := NewAggregate(queryStr, config.Server.MapreduceLogFormat)
		if err != nil {
			t.Fatalf("NewAggregate failed: %v", err)
		}

		messages := make(chan string, 8)
		ctx, cancel := context.WithCancel(context.Background())

		// Release both goroutines as close together as possible so the ticker
		// Store near the top of Start overlaps the Load inside
		// stopSerializeTicker. Abort is used because it reaches
		// stopSerializeTicker without waiting for a final serialization, giving
		// the tightest overlap with Start's ticker publish.
		release := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-release
			agg.Start(ctx, messages)
		}()
		go func() {
			defer wg.Done()
			<-release
			agg.Abort()
		}()
		close(release)

		// Abort signals done, so Start unblocks its select and its serialization
		// loop exits. Cancel as a belt-and-suspenders in case Abort lost the race
		// and Start is still waiting on the ticker interval.
		cancel()
		wg.Wait()

		close(messages)
		for range messages { //nolint:revive // drain any (unexpected) output
		}
	}
}

func waitForAggregateStart(t *testing.T, aggregate *Aggregate) {
	t.Helper()

	if aggregate.started == nil {
		t.Fatal("aggregate missing start signal")
	}
	select {
	case <-aggregate.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("aggregate did not finish Start initialization")
	}
}
