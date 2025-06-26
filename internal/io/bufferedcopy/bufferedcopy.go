// Package bufferedcopy provides a safe bidirectional copy operation that prevents deadlocks
package bufferedcopy

import (
	"context"
	"io"
	"sync"
)

// BidirectionalCopy performs bidirectional copying between two io.ReadWriter interfaces
// using goroutines and channels to prevent deadlocks that occur with direct io.Copy
func BidirectionalCopy(ctx context.Context, a, b io.ReadWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	
	errChan := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	
	// Copy from A to B
	go func() {
		defer wg.Done()
		err := copyWithContext(ctx, b, a)
		if err != nil && err != context.Canceled {
			errChan <- err
			cancel()
		}
	}()
	
	// Copy from B to A
	go func() {
		defer wg.Done()
		err := copyWithContext(ctx, a, b)
		if err != nil && err != context.Canceled {
			errChan <- err
			cancel()
		}
	}()
	
	// Wait for completion
	wg.Wait()
	close(errChan)
	
	// Return first error if any
	for err := range errChan {
		return err
	}
	
	return nil
}

// copyWithContext performs io.Copy with context cancellation support
func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) error {
	// Use a reasonable buffer size
	buf := make([]byte, 32*1024)
	
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		
		nr, readErr := src.Read(buf)
		if nr > 0 {
			nw, writeErr := dst.Write(buf[:nr])
			if writeErr != nil {
				return writeErr
			}
			if nw != nr {
				return io.ErrShortWrite
			}
		}
		
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}