package server

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/source"
)

// ensureTestServerConfig initialises the minimum globals required by turbo
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
			TurboBoostDisable:  false,
		}
	}
	if dlog.Server == nil {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		var wg sync.WaitGroup
		wg.Add(1)
		dlog.Start(ctx, &wg, source.Server)
	}
}

// TestTurboAggregateDoSerializeReMergesOnCtxCancel verifies that when the
// serialize context is cancelled after at least one send succeeded, the
// remaining aggregate sets are re-merged back into a.groupSets instead of
// being silently dropped. This is a regression test for the data-loss bug
// where swapGroupSets removed state from the live map and a cancelled ctx
// caused the snapshot to be discarded.
func TestTurboAggregateDoSerializeReMergesOnCtxCancel(t *testing.T) {
	ensureTestServerConfig(t)

	queryStr := `from STATS select count($time),$time from - group by $time`
	turboAgg, err := NewTurboAggregate(queryStr, config.Server.MapreduceLogFormat)
	if err != nil {
		t.Fatalf("NewTurboAggregate failed: %v", err)
	}

	// Pre-populate the aggregate state directly with several groups so the
	// serialize loop must emit multiple messages.
	const numGroups = 5
	turboAgg.groupMu.Lock()
	for i := 0; i < numGroups; i++ {
		key := fmt.Sprintf("g%d", i)
		set := mapr.NewAggregateSet()
		if err := set.Aggregate("count($time)", mapr.Count, "1", false); err != nil {
			turboAgg.groupMu.Unlock()
			t.Fatalf("seed aggregate failed: %v", err)
		}
		set.Samples = 1
		turboAgg.groupSets[key] = set
	}
	turboAgg.groupMu.Unlock()

	if got := turboAgg.countGroups(); got != numGroups {
		t.Fatalf("precondition: expected %d groups, got %d", numGroups, got)
	}

	// Wire maprMessages with a consumer that only receives the first message.
	// This forces doSerialize to make progress, then block on the second send
	// until the context is cancelled.
	messages := make(chan string)
	turboAgg.maprMessages = messages

	ctx, cancel := context.WithCancel(context.Background())
	firstSent := make(chan struct{})
	go func() {
		<-messages
		close(firstSent)
	}()
	done := make(chan struct{})
	go func() {
		turboAgg.doSerialize(ctx)
		close(done)
	}()

	select {
	case <-firstSent:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first serialized message")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("doSerialize did not return after ctx cancel")
	}

	if got := turboAgg.countGroups(); got != numGroups-1 {
		t.Fatalf("expected %d groups re-merged after ctx cancel, got %d", numGroups-1, got)
	}

	// Collect whatever might have drained beyond the first message (should be
	// none) to make sure we do not leak the goroutine.
	select {
	case msg := <-messages:
		t.Fatalf("unexpected additional message drained: %q", msg)
	default:
	}
}

func TestTurboAggregateVsRegular(t *testing.T) {
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
			TurboBoostDisable:  false,
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

	t.Run("TurboAggregate", func(t *testing.T) {
		// Create turbo aggregate
		turboAgg, err := NewTurboAggregate(queryStr, config.Server.MapreduceLogFormat)
		if err != nil {
			t.Fatalf("Failed to create turbo aggregate: %v", err)
		}

		// Channel to collect messages
		messages := make(chan string, 100)
		// Use a cancellable context
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		startDone := make(chan struct{})
		go func() {
			defer close(startDone)
			turboAgg.Start(ctx, messages)
		}()
		waitForTurboAggregateStart(t, turboAgg)

		// Process lines
		processor := NewTurboAggregateProcessor(turboAgg, "test")
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
		turboAgg.Shutdown()

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

		t.Logf("Turbo mode processed %d lines", turboAgg.linesProcessed.Load())
		t.Logf("Turbo mode results: %d messages", len(results))
		for _, r := range results {
			t.Logf("Result: %s", r)
		}

		// Verify we got results
		if len(results) == 0 {
			t.Error("Turbo mode produced no results")
		}

		// Check line count
		if turboAgg.linesProcessed.Load() != uint64(len(testLines)) {
			t.Errorf("Expected %d lines processed, got %d", len(testLines), turboAgg.linesProcessed.Load())
		}
	})

	t.Run("RegularAggregate", func(t *testing.T) {
		// Create regular aggregate
		regularAgg, err := NewAggregate(queryStr, config.Server.MapreduceLogFormat)
		if err != nil {
			t.Fatalf("Failed to create regular aggregate: %v", err)
		}

		// Channel to collect messages
		messages := make(chan string, 100)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Start the regular aggregate in a goroutine
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			regularAgg.Start(ctx, messages)
		}()

		// Give it time to start
		time.Sleep(50 * time.Millisecond)

		// Create line channel
		lines := make(chan *line.Line, 100)
		regularAgg.NextLinesCh <- lines

		// Process lines
		for _, lineStr := range testLines {
			l := &line.Line{
				Content:  bytes.NewBufferString(lineStr),
				SourceID: "test",
			}
			lines <- l
		}
		close(lines)

		// Wait for the aggregate to drain the closed line channel and serialize naturally.
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			regularAgg.Shutdown()
			cancel()
			t.Fatal("Timeout waiting for regular aggregate to finish")
		}
		cancel()

		// Collect results
		close(messages)

		var results []string
		for msg := range messages {
			results = append(results, msg)
		}

		t.Logf("Regular mode results: %d messages", len(results))
		for _, r := range results {
			t.Logf("Result: %s", r)
		}

		// Verify we got results
		if len(results) == 0 {
			t.Error("Regular mode produced no results")
		}
	})
}

// TestTurboAggregateConcurrency tests turbo aggregate with concurrent file processing
func TestTurboAggregateConcurrency(t *testing.T) {
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
			TurboBoostDisable:  false,
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

	// Create turbo aggregate
	turboAgg, err := NewTurboAggregate(queryStr, config.Server.MapreduceLogFormat)
	if err != nil {
		t.Fatalf("Failed to create turbo aggregate: %v", err)
	}

	// Channel to collect messages
	messages := make(chan string, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		turboAgg.Start(ctx, messages)
	}()
	waitForTurboAggregateStart(t, turboAgg)

	// Process multiple "files" concurrently
	var wg sync.WaitGroup
	numFiles := 10
	linesPerFile := 100

	for f := 0; f < numFiles; f++ {
		wg.Add(1)
		go func(fileNum int) {
			defer wg.Done()

			processor := NewTurboAggregateProcessor(turboAgg, "file"+string(rune(fileNum)))

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
	turboAgg.Shutdown()
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

	t.Logf("Processed %d lines total", turboAgg.linesProcessed.Load())
	t.Logf("Processed %d files", turboAgg.filesProcessed.Load())
	t.Logf("Got %d result messages", len(results))

	// Verify line count
	expectedLines := uint64(numFiles * linesPerFile)
	if turboAgg.linesProcessed.Load() != expectedLines {
		t.Errorf("Expected %d lines processed, got %d", expectedLines, turboAgg.linesProcessed.Load())
	}

	if turboAgg.filesProcessed.Load() != uint64(numFiles) {
		t.Errorf("Expected %d files processed, got %d", numFiles, turboAgg.filesProcessed.Load())
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

func TestTurboAggregateAbortReturnsPromptlyWithActiveProcessors(t *testing.T) {
	aggregate := &TurboAggregate{}
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

func TestTurboAggregateProcessorCountsFlushOnce(t *testing.T) {
	aggregate := &TurboAggregate{
		done:      internal.NewDone(),
		batchSize: 16,
	}

	processor := NewTurboAggregateProcessor(aggregate, "test")
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

func waitForTurboAggregateStart(t *testing.T, aggregate *TurboAggregate) {
	t.Helper()

	if aggregate.started == nil {
		t.Fatal("turbo aggregate missing start signal")
	}
	select {
	case <-aggregate.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("turbo aggregate did not finish Start initialization")
	}
}
