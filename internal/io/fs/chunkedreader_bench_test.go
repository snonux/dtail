package fs

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/pool"
)

// BenchmarkChunkedReader tests the performance of the chunked reader
func BenchmarkChunkedReader(b *testing.B) {
	// Create test data - simulate a log file with many lines
	var testData strings.Builder
	for i := 0; i < 10000; i++ {
		testData.WriteString("INFO|1002-071143|1|stats.go:56|8|13|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1\n")
	}
	data := testData.String()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		reader := strings.NewReader(data)
		chunkedReader := NewChunkedReader(reader, 64*1024)
		
		rawLines := make(chan *bytes.Buffer, 100)
		serverMessages := make(chan string, 10)
		
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		
		go func() {
			defer cancel()
			err := chunkedReader.ProcessLines(ctx, rawLines, 1000, "test.log", serverMessages, false)
			if err != nil {
				b.Errorf("ProcessLines error: %v", err)
			}
			close(rawLines)
		}()
		
		// Consume all lines
		lineCount := 0
		for line := range rawLines {
			lineCount++
			pool.BytesBuffer.Put(line)
		}
		
		cancel()
		
		if lineCount != 10000 {
			b.Errorf("Expected 10000 lines, got %d", lineCount)
		}
	}
}

// BenchmarkChunkedReaderSmall tests performance with smaller chunks
func BenchmarkChunkedReaderSmall(b *testing.B) {
	// Create smaller test data
	var testData strings.Builder
	for i := 0; i < 1000; i++ {
		testData.WriteString("INFO|1002-071143|1|stats.go:56|8|13|7|0.21|471h0m21s|MAPREDUCE:STATS|currentConnections=0|lifetimeConnections=1\n")
	}
	data := testData.String()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		reader := strings.NewReader(data)
		chunkedReader := NewChunkedReader(reader, 4*1024) // 4KB chunks
		
		rawLines := make(chan *bytes.Buffer, 100)
		serverMessages := make(chan string, 10)
		
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		
		go func() {
			defer cancel()
			err := chunkedReader.ProcessLines(ctx, rawLines, 1000, "test.log", serverMessages, false)
			if err != nil {
				b.Errorf("ProcessLines error: %v", err)
			}
			close(rawLines)
		}()
		
		// Consume all lines
		lineCount := 0
		for line := range rawLines {
			lineCount++
			pool.BytesBuffer.Put(line)
		}
		
		cancel()
		
		if lineCount != 1000 {
			b.Errorf("Expected 1000 lines, got %d", lineCount)
		}
	}
}