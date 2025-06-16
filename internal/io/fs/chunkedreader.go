package fs

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/pool"
)

// Reusable timer to reduce allocations - PBO optimization
var sharedTimer = time.NewTimer(10 * time.Millisecond)

// ChunkedReader reads data in large chunks and processes it line by line
// This replaces the byte-by-byte reading approach for better performance
type ChunkedReader struct {
	reader     io.Reader
	buffer     []byte
	remaining  []byte // Partial line from previous chunk
	chunkSize  int
	eof        bool
	// PBO optimization: Pre-allocate line buffer to reduce allocations
	lineBuffer []byte
	lineLen    int
}

// NewChunkedReader creates a new chunked reader with the specified chunk size
func NewChunkedReader(reader io.Reader, chunkSize int) *ChunkedReader {
	if chunkSize <= 0 {
		chunkSize = 64 * 1024 // Default 64KB chunks
	}
	return &ChunkedReader{
		reader:     reader,
		buffer:     make([]byte, chunkSize),
		chunkSize:  chunkSize,
		// PBO optimization: Pre-allocate line buffer
		lineBuffer: make([]byte, 0, 8192), // 8KB initial capacity
	}
}

// ProcessLines reads data in chunks and processes it line by line, sending complete lines
// to the rawLines channel. This mimics the behavior of the original byte-by-byte approach.
func (cr *ChunkedReader) ProcessLines(ctx context.Context, rawLines chan *bytes.Buffer, 
	maxLineLength int, filePath string, serverMessages chan<- string, seekEOF bool) error {
	
	message := pool.BytesBuffer.Get().(*bytes.Buffer)
	warnedAboutLongLine := false
	
	for {
		// Read next chunk if we don't have remaining data
		if len(cr.remaining) == 0 && !cr.eof {
			n, err := cr.reader.Read(cr.buffer)
			if err != nil {
				if err == io.EOF {
					if !seekEOF {
						// Not in tailing mode - end of file means we're done
						cr.eof = true
						if message.Len() > 0 {
							// Send any remaining data as the last line
							select {
							case rawLines <- message:
							case <-ctx.Done():
								return ctx.Err()
							}
						}
						return nil
					} else {
						// In tailing mode - EOF means wait and try again
						// Use shared timer to reduce allocations - PBO optimization
						if !sharedTimer.Stop() {
							// Drain timer channel if it fired
							select {
							case <-sharedTimer.C:
							default:
							}
						}
						sharedTimer.Reset(10 * time.Millisecond)
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-sharedTimer.C:
							// Continue reading after brief pause
							continue
						}
					}
				}
				return err
			}
			// Combine any leftover partial line with new data
			if message.Len() > 0 {
				// We had a partial line from previous iteration
				newData := make([]byte, message.Len()+n)
				copy(newData, message.Bytes())
				copy(newData[message.Len():], cr.buffer[:n])
				cr.remaining = newData
				message.Reset()
			} else {
				cr.remaining = cr.buffer[:n]
			}
		}
		
		// If we have no more data and reached EOF, we're done
		if len(cr.remaining) == 0 && cr.eof {
			if message.Len() > 0 {
				select {
				case rawLines <- message:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		
		// Process data and extract complete lines - PBO optimized
		// Reset line buffer for this chunk
		cr.lineBuffer = cr.lineBuffer[:0]
		cr.lineLen = 0
		
		for _, b := range cr.remaining {
			// Use pre-allocated buffer to reduce byte-by-byte WriteByte calls
			if cr.lineLen < len(cr.lineBuffer) {
				cr.lineBuffer[cr.lineLen] = b
			} else {
				cr.lineBuffer = append(cr.lineBuffer, b)
			}
			cr.lineLen++
			
			switch b {
			case '\n':
				// Send the complete line using Write for bulk operation
				message.Write(cr.lineBuffer[:cr.lineLen])
				select {
				case rawLines <- message:
					message = pool.BytesBuffer.Get().(*bytes.Buffer)
					warnedAboutLongLine = false
					// Reset line buffer for next line
					cr.lineLen = 0
				case <-ctx.Done():
					return ctx.Err()
				}
			default:
				// Check line length limit
				if cr.lineLen >= maxLineLength {
					if !warnedAboutLongLine {
						serverMessages <- dlog.Common.Warn(filePath,
							"Long log line, splitting into multiple lines") + "\n"
						warnedAboutLongLine = true
					}
					// Add newline to current buffer and send
					if cr.lineLen < len(cr.lineBuffer) {
						cr.lineBuffer[cr.lineLen] = '\n'
					} else {
						cr.lineBuffer = append(cr.lineBuffer, '\n')
					}
					cr.lineLen++
					message.Write(cr.lineBuffer[:cr.lineLen])
					select {
					case rawLines <- message:
						message = pool.BytesBuffer.Get().(*bytes.Buffer)
						// Reset line buffer for next line
						cr.lineLen = 0
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
		
		// If we have remaining data in line buffer, add it to message
		if cr.lineLen > 0 {
			message.Write(cr.lineBuffer[:cr.lineLen])
		}
		
		// Clear the remaining buffer - any partial line is now in the message buffer
		cr.remaining = nil
	}
}