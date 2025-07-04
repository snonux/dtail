package clients

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/source"
	"sync"
)

func setupBenchmarkData(b *testing.B, lines int) string {
	b.Helper()
	
	tmpDir := b.TempDir()
	testFile := filepath.Join(tmpDir, "benchmark_data.log")
	
	f, err := os.Create(testFile)
	if err != nil {
		b.Fatalf("Failed to create test file: %v", err)
	}
	defer f.Close()
	
	// Create test data
	for i := 0; i < lines; i++ {
		line := fmt.Sprintf("INFO|1002-071143|1|test.go:%d|8|%d|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=%d|lifetimeConnections=%d|pattern=test-%d|data=%s\n",
			i%100, i%50, i%10, i, i%5, "some-test-data-that-makes-the-line-longer")
		f.WriteString(line)
	}
	
	return testFile
}

func BenchmarkDGrepTurboEnabled(b *testing.B) {
	benchmarkDGrep(b, false)
}

func BenchmarkDGrepTurboDisabled(b *testing.B) {
	benchmarkDGrep(b, true)
}

func benchmarkDGrep(b *testing.B, disableTurbo bool) {
	// Setup config
	config.Server = &config.ServerConfig{
		TurboBoostDisable:  disableTurbo,
		MaxConcurrentCats:  10,
		MaxConcurrentTails: 50,
		MaxLineLength:      1024 * 1024,
	}
	
	config.Common = &config.CommonConfig{
		Logger:   "none",
		LogLevel: "error",
	}
	
	config.Client = &config.ClientConfig{
		TermColorsEnable: false,
	}
	
	// Initialize logging
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wg := &sync.WaitGroup{}
	wg.Add(1)
	dlog.Start(ctx, wg, source.Client)
	
	// Create test data
	testFile := setupBenchmarkData(b, 100000) // 100k lines
	
	b.ResetTimer()
	
	for i := 0; i < b.N; i++ {
		// Create grep client
		args := config.Args{
			ServersStr: "serverless",
			QueryStr:   "",
			What:       testFile,
			RegexStr:   "pattern=test-1",
			Serverless: true,
			Plain:      true,
		}
		
		client, err := NewGrepClient(args)
		if err != nil {
			b.Fatalf("Failed to create grep client: %v", err)
		}
		
		// Capture output
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w
		
		// Run grep
		statusCh := make(chan int, 1)
		go func() {
			status := client.Start(ctx, nil) // nil for statsCh
			statusCh <- status
		}()
		
		// Wait for completion or timeout
		select {
		case status := <-statusCh:
			if status != 0 {
				b.Errorf("Grep failed with status: %d", status)
			}
		case <-time.After(30 * time.Second):
			b.Error("Grep timed out")
		}
		
		// Restore stdout
		w.Close()
		os.Stdout = oldStdout
		
		// Read captured output
		var buf bytes.Buffer
		buf.ReadFrom(r)
	}
}

// Benchmark with different file sizes
func BenchmarkDGrepTurboSmallFile(b *testing.B) {
	benchmarkDGrepWithSize(b, false, 1000) // 1k lines
}

func BenchmarkDGrepTurboDisabledSmallFile(b *testing.B) {
	benchmarkDGrepWithSize(b, true, 1000) // 1k lines
}

func BenchmarkDGrepTurboMediumFile(b *testing.B) {
	benchmarkDGrepWithSize(b, false, 50000) // 50k lines
}

func BenchmarkDGrepTurboDisabledMediumFile(b *testing.B) {
	benchmarkDGrepWithSize(b, true, 50000) // 50k lines
}

func BenchmarkDGrepTurboLargeFile(b *testing.B) {
	benchmarkDGrepWithSize(b, false, 500000) // 500k lines
}

func BenchmarkDGrepTurboDisabledLargeFile(b *testing.B) {
	benchmarkDGrepWithSize(b, true, 500000) // 500k lines
}

func benchmarkDGrepWithSize(b *testing.B, disableTurbo bool, lines int) {
	// Setup config
	config.Server = &config.ServerConfig{
		TurboBoostDisable:  disableTurbo,
		MaxConcurrentCats:  10,
		MaxConcurrentTails: 50,
		MaxLineLength:      1024 * 1024,
	}
	
	config.Common = &config.CommonConfig{
		Logger:   "none",
		LogLevel: "error",
	}
	
	config.Client = &config.ClientConfig{
		TermColorsEnable: false,
	}
	
	// Initialize logging
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wg := &sync.WaitGroup{}
	wg.Add(1)
	dlog.Start(ctx, wg, source.Client)
	
	// Create test data
	testFile := setupBenchmarkData(b, lines)
	
	b.ResetTimer()
	
	for i := 0; i < b.N; i++ {
		// Create grep client
		args := config.Args{
			ServersStr: "serverless",
			QueryStr:   "",
			What:       testFile,
			RegexStr:   "pattern=test-1",
			Serverless: true,
			Plain:      true,
		}
		
		client, err := NewGrepClient(args)
		if err != nil {
			b.Fatalf("Failed to create grep client: %v", err)
		}
		
		// Capture output
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w
		
		// Run grep
		statusCh := make(chan int, 1)
		go func() {
			status := client.Start(ctx, nil) // nil for statsCh
			statusCh <- status
		}()
		
		// Wait for completion or timeout
		select {
		case status := <-statusCh:
			if status != 0 {
				b.Errorf("Grep failed with status: %d", status)
			}
		case <-time.After(30 * time.Second):
			b.Error("Grep timed out")
		}
		
		// Restore stdout
		w.Close()
		os.Stdout = oldStdout
		
		// Read captured output
		var buf bytes.Buffer
		buf.ReadFrom(r)
	}
	
	// Report custom metrics
	b.ReportMetric(float64(lines), "lines/op")
	b.ReportMetric(float64(lines)/b.Elapsed().Seconds(), "lines/sec")
}