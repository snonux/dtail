// Package bufferedpipe provides a bidirectional pipe with buffering to prevent deadlocks
package bufferedpipe

import (
	"io"
	"sync"
)

// BufferedPipe provides bidirectional data transfer with buffering to prevent deadlocks
// that can occur with direct io.Copy operations in opposite directions.
type BufferedPipe struct {
	bufferSize int
	done       chan struct{}
	once       sync.Once
}

// New creates a new BufferedPipe with the specified buffer size for each direction
func New(bufferSize int) *BufferedPipe {
	return &BufferedPipe{
		bufferSize: bufferSize,
		done:       make(chan struct{}),
	}
}

// ConnectBidirectional connects two io.ReadWriter interfaces bidirectionally
// It returns when either side closes or an error occurs
func (bp *BufferedPipe) ConnectBidirectional(a, b io.ReadWriter) error {
	var wg sync.WaitGroup
	wg.Add(2)
	
	errChan := make(chan error, 2)
	
	// Create buffered channels for data transfer
	aToB := make(chan []byte, 10)
	bToA := make(chan []byte, 10)
	
	// Goroutine to handle shutdown
	shutdown := make(chan struct{})
	go func() {
		select {
		case <-bp.done:
		case <-shutdown:
		}
		close(aToB)
		close(bToA)
	}()
	
	// Copy from A to B with buffering
	go func() {
		defer wg.Done()
		defer close(shutdown)
		
		buffer := make([]byte, bp.bufferSize)
		for {
			select {
			case <-bp.done:
				return
			default:
			}
			
			n, err := a.Read(buffer)
			if err != nil {
				if err != io.EOF {
					errChan <- err
				}
				return
			}
			
			if n > 0 {
				data := make([]byte, n)
				copy(data, buffer[:n])
				
				select {
				case aToB <- data:
				case <-bp.done:
					return
				}
			}
		}
	}()
	
	// Copy from B to A with buffering
	go func() {
		defer wg.Done()
		
		buffer := make([]byte, bp.bufferSize)
		for {
			select {
			case <-bp.done:
				return
			default:
			}
			
			n, err := b.Read(buffer)
			if err != nil {
				if err != io.EOF {
					errChan <- err
				}
				return
			}
			
			if n > 0 {
				data := make([]byte, n)
				copy(data, buffer[:n])
				
				select {
				case bToA <- data:
				case <-bp.done:
					return
				}
			}
		}
	}()
	
	// Writer goroutines
	go func() {
		for data := range aToB {
			_, err := b.Write(data)
			if err != nil {
				errChan <- err
				return
			}
		}
	}()
	
	go func() {
		for data := range bToA {
			_, err := a.Write(data)
			if err != nil {
				errChan <- err
				return
			}
		}
	}()
	
	// Wait for completion
	go func() {
		wg.Wait()
		close(errChan)
	}()
	
	// Return first error if any
	for err := range errChan {
		bp.Close()
		return err
	}
	
	return nil
}

// Close closes the BufferedPipe
func (bp *BufferedPipe) Close() error {
	bp.once.Do(func() {
		close(bp.done)
	})
	return nil
}

// CopyBuffered performs a single direction copy with buffering to prevent deadlocks
// This is a simpler alternative when only one direction is needed
func CopyBuffered(dst io.Writer, src io.Reader, bufferSize int) (int64, error) {
	// Use a goroutine with a channel to buffer the data
	dataChan := make(chan []byte, 10)
	errChan := make(chan error, 1)
	
	// Reader goroutine
	go func() {
		defer close(dataChan)
		buffer := make([]byte, bufferSize)
		
		for {
			n, err := src.Read(buffer)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buffer[:n])
				dataChan <- data
			}
			if err != nil {
				if err != io.EOF {
					errChan <- err
				}
				return
			}
		}
	}()
	
	// Writer (main goroutine)
	var written int64
	for data := range dataChan {
		n, err := dst.Write(data)
		written += int64(n)
		if err != nil {
			return written, err
		}
	}
	
	// Check for read errors
	select {
	case err := <-errChan:
		return written, err
	default:
		return written, nil
	}
}