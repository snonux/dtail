package server

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/source"
)

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
			TurboModeEnable: false,
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
		turboAgg, err := NewTurboAggregate(queryStr)
		if err != nil {
			t.Fatalf("Failed to create turbo aggregate: %v", err)
		}
		
		// Channel to collect messages
		messages := make(chan string, 100)
		// Use a cancellable context
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		
		// Start the turbo aggregate
		turboAgg.Start(ctx, messages)
		
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
		regularAgg, err := NewAggregate(queryStr)
		if err != nil {
			t.Fatalf("Failed to create regular aggregate: %v", err)
		}
		
		// Channel to collect messages
		messages := make(chan string, 100)
		ctx := context.Background()
		
		// Start the regular aggregate
		regularAgg.Start(ctx, messages)
		
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
		
		// Wait for processing
		time.Sleep(100 * time.Millisecond)
		
		// Shutdown and get results
		regularAgg.Shutdown()
		
		// Collect results
		time.Sleep(100 * time.Millisecond)
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
			TurboModeEnable: false,
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
	turboAgg, err := NewTurboAggregate(queryStr)
	if err != nil {
		t.Fatalf("Failed to create turbo aggregate: %v", err)
	}
	
	// Channel to collect messages
	messages := make(chan string, 1000)
	ctx := context.Background()
	
	// Start the turbo aggregate
	turboAgg.Start(ctx, messages)
	
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
		}(f)
	}
	
	// Wait for all files to complete
	wg.Wait()
	
	// Shutdown and get results
	turboAgg.Shutdown()
	
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
	
	// Verify file count
	if turboAgg.filesProcessed.Load() != uint64(numFiles) {
		t.Errorf("Expected %d files processed, got %d", numFiles, turboAgg.filesProcessed.Load())
	}
	
	// Parse result to check count
	for _, result := range results {
		t.Logf("Result: %s", result)
		// The result should show count=1000 (10 files * 100 lines each)
		if strings.Contains(result, "1000,1002-071143") {
			t.Log("✓ Found expected count of 1000")
			return
		}
	}
	
	t.Error("Did not find expected count of 1000 in results")
}