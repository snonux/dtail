package bufferedpipe

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// TestBufferedPipeNoDeadlock verifies that BufferedPipe prevents deadlocks
func TestBufferedPipeNoDeadlock(t *testing.T) {
	tests := []struct {
		name       string
		dataSize   int
		bufferSize int
	}{
		{
			name:       "small_data",
			dataSize:   1024,
			bufferSize: 4096,
		},
		{
			name:       "exact_buffer",
			dataSize:   4096,
			bufferSize: 4096,
		},
		{
			name:       "large_data",
			dataSize:   1024 * 1024, // 1MB
			bufferSize: 64 * 1024,   // 64KB buffer
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test data
			testData := bytes.Repeat([]byte("x"), tt.dataSize)
			
			// Create two buffers to act as endpoints
			var bufA, bufB bytes.Buffer
			bufA.Write(testData)
			
			// Create buffered pipe
			bp := New(tt.bufferSize)
			
			// Set up completion tracking
			done := make(chan error, 1)
			
			go func() {
				err := bp.ConnectBidirectional(&bufA, &bufB)
				done <- err
			}()
			
			// Wait for completion or timeout
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				// Verify data was transferred
				if bufB.Len() != tt.dataSize {
					t.Errorf("Data size mismatch: got %d, want %d", bufB.Len(), tt.dataSize)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Operation timed out - possible deadlock")
			}
		})
	}
}

// TestBidirectionalTransfer tests simultaneous bidirectional data transfer
func TestBidirectionalTransfer(t *testing.T) {
	dataA := bytes.Repeat([]byte("A"), 100*1024) // 100KB from A
	dataB := bytes.Repeat([]byte("B"), 100*1024) // 100KB from B
	
	// Create endpoints
	endpointA := &mockEndpoint{
		toSend:   dataA,
		received: new(bytes.Buffer),
	}
	endpointB := &mockEndpoint{
		toSend:   dataB,
		received: new(bytes.Buffer),
	}
	
	// Create buffered pipe
	bp := New(32 * 1024) // 32KB buffer
	
	// Connect bidirectionally
	done := make(chan error, 1)
	go func() {
		err := bp.ConnectBidirectional(endpointA, endpointB)
		done <- err
	}()
	
	// Wait for completion
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Bidirectional transfer timed out")
	}
	
	// Verify data was exchanged correctly
	if !bytes.Equal(endpointA.received.Bytes(), dataB) {
		t.Error("Endpoint A did not receive correct data from B")
	}
	if !bytes.Equal(endpointB.received.Bytes(), dataA) {
		t.Error("Endpoint B did not receive correct data from A")
	}
}

// mockEndpoint simulates a bidirectional endpoint
type mockEndpoint struct {
	toSend   []byte
	sendPos  int
	received *bytes.Buffer
	mu       sync.Mutex
}

func (m *mockEndpoint) Read(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.sendPos >= len(m.toSend) {
		return 0, io.EOF
	}
	
	n := copy(p, m.toSend[m.sendPos:])
	m.sendPos += n
	return n, nil
}

func (m *mockEndpoint) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.received.Write(p)
}

// BenchmarkBufferedPipe benchmarks the buffered pipe performance
func BenchmarkBufferedPipe(b *testing.B) {
	sizes := []int{
		1024,       // 1KB
		64 * 1024,  // 64KB
		1024 * 1024, // 1MB
	}
	
	for _, size := range sizes {
		b.Run(fmt.Sprintf("size_%dKB", size/1024), func(b *testing.B) {
			testData := bytes.Repeat([]byte("x"), size)
			
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var bufA, bufB bytes.Buffer
				bufA.Write(testData)
				
				bp := New(64 * 1024) // 64KB buffer
				
				done := make(chan error, 1)
				go func() {
					err := bp.ConnectBidirectional(&bufA, &bufB)
					done <- err
				}()
				
				select {
				case <-done:
					// Success
				case <-time.After(1 * time.Second):
					b.Fatal("Transfer timed out")
				}
			}
		})
	}
}

// TestPipeBufferOperations tests the internal pipe buffer operations
func TestPipeBufferOperations(t *testing.T) {
	p := newPipe(10) // Small buffer for testing
	
	// Test write and read
	data := []byte("hello")
	n, err := p.write(data)
	if err != nil || n != len(data) {
		t.Fatalf("Write failed: %v, wrote %d bytes", err, n)
	}
	
	buf := make([]byte, 10)
	n, err = p.read(buf)
	if err != nil || n != len(data) {
		t.Fatalf("Read failed: %v, read %d bytes", err, n)
	}
	
	if !bytes.Equal(buf[:n], data) {
		t.Errorf("Data mismatch: got %s, want %s", buf[:n], data)
	}
	
	// Test wrap-around
	data2 := []byte("world123") // This will wrap around
	n, err = p.write(data2)
	if err != nil || n != len(data2) {
		t.Fatalf("Write wrap-around failed: %v, wrote %d bytes", err, n)
	}
	
	n, err = p.read(buf)
	if err != nil || n != len(data2) {
		t.Fatalf("Read wrap-around failed: %v, read %d bytes", err, n)
	}
	
	if !bytes.Equal(buf[:n], data2) {
		t.Errorf("Wrap-around data mismatch: got %s, want %s", buf[:n], data2)
	}
	
	// Test close
	p.close()
	_, err = p.read(buf)
	if err != io.EOF {
		t.Errorf("Expected EOF after close, got %v", err)
	}
}