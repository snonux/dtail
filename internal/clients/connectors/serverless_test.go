package connectors

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
	
	"github.com/mimecast/dtail/internal/io/bufferedpipe"
)

// TestServerlessDeadlockSimple demonstrates the deadlock issue with io.Copy
func TestServerlessDeadlockSimple(t *testing.T) {
	// This test demonstrates the deadlock that occurs with bidirectional io.Copy
	// when buffers fill up on both sides
	
	// Create two pipes to simulate the handler connections
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()
	
	// Buffer to track completion
	var wg sync.WaitGroup
	wg.Add(2)
	
	// Simulate the problematic io.Copy pattern from serverless.go
	go func() {
		defer wg.Done()
		// This simulates: io.Copy(serverHandler, s.handler)
		io.Copy(w2, r1)
	}()
	
	go func() {
		defer wg.Done()
		// This simulates: io.Copy(s.handler, serverHandler)
		io.Copy(w1, r2)
	}()
	
	// Try to write a large amount of data
	dataSize := 512 * 1024 // 512KB
	testData := bytes.Repeat([]byte("x"), dataSize)
	
	done := make(chan bool)
	go func() {
		// Try to write data
		w1.Write(testData)
		w1.Close()
		w2.Close()
		wg.Wait()
		done <- true
	}()
	
	// Wait for completion or timeout
	select {
	case <-done:
		t.Error("Expected deadlock but completed successfully")
	case <-time.After(2 * time.Second):
		// Expected behavior with current implementation
		t.Log("Confirmed: bidirectional io.Copy causes deadlock with large data")
	}
}

// TestBufferedPipeNoDeadlock tests that our fix prevents deadlocks
func TestBufferedPipeNoDeadlock(t *testing.T) {
	// Test the buffered pipe approach
	bp := bufferedpipe.New(64 * 1024) // 64KB buffer
	
	// Create two pipes to simulate handler connections
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()
	
	// Create adapters
	adapter1 := &pipeAdapter{r: r1, w: w2}
	adapter2 := &pipeAdapter{r: r2, w: w1}
	
	// Large data that would cause deadlock with direct io.Copy
	dataSize := 512 * 1024 // 512KB
	testData := bytes.Repeat([]byte("x"), dataSize)
	
	done := make(chan bool)
	go func() {
		// Write large data
		adapter1.Write(testData)
		w1.Close()
		w2.Close()
		done <- true
	}()
	
	// Connect with buffered pipe
	go func() {
		bp.ConnectBidirectional(adapter1, adapter2)
	}()
	
	// Read the data
	result := make([]byte, dataSize)
	go func() {
		io.ReadFull(adapter2, result)
	}()
	
	// Should complete without deadlock
	select {
	case <-done:
		t.Log("Success: BufferedPipe prevented deadlock with large data")
	case <-time.After(5 * time.Second):
		t.Error("BufferedPipe operation timed out - possible deadlock")
	}
}

// pipeAdapter adapts separate read/write pipes to io.ReadWriter
type pipeAdapter struct {
	r io.Reader
	w io.Writer
}

func (p *pipeAdapter) Read(b []byte) (int, error) {
	return p.r.Read(b)
}

func (p *pipeAdapter) Write(b []byte) (int, error) {
	return p.w.Write(b)
}

// BenchmarkIOCopyDeadlock measures when deadlock occurs
func BenchmarkIOCopyDeadlock(b *testing.B) {
	sizes := []int{
		1024,        // 1KB - should work
		64 * 1024,   // 64KB - should work (below typical pipe buffer)
		65 * 1024,   // 65KB - might deadlock
		128 * 1024,  // 128KB - likely deadlock
	}
	
	for _, size := range sizes {
		b.Run(fmt.Sprintf("size_%dKB", size/1024), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				r1, w1 := io.Pipe()
				r2, w2 := io.Pipe()
				
				testData := bytes.Repeat([]byte("x"), size)
				success := make(chan bool, 1)
				
				go func() {
					io.Copy(w2, r1)
				}()
				
				go func() {
					io.Copy(w1, r2)
				}()
				
				go func() {
					w1.Write(testData)
					w1.Close()
					w2.Close()
					success <- true
				}()
				
				select {
				case <-success:
					// Completed successfully
				case <-time.After(100 * time.Millisecond):
					// Deadlock detected
					b.Logf("Deadlock at size %dKB", size/1024)
				}
			}
		})
	}
}