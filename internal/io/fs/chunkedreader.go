package fs

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/pool"
)

// ChunkedReader reads data in large chunks and processes it line by line
// This replaces the byte-by-byte reading approach for better performance
type ChunkedReader struct {
	reader     io.Reader
	buffer     []byte
	remaining  []byte // Partial line from previous chunk
	chunkSize  int
	eof        bool
}

// NewChunkedReader creates a new chunked reader with the specified chunk size
func NewChunkedReader(reader io.Reader, chunkSize int) *ChunkedReader {
	if chunkSize <= 0 {
		chunkSize = 64 * 1024 // Default 64KB chunks
	}
	return &ChunkedReader{
		reader:    reader,
		buffer:    make([]byte, chunkSize),
		chunkSize: chunkSize,
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
						// This mimics the original behavior of sleeping 100ms on EOF
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(100 * time.Millisecond):
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
		
		// Process data and extract complete lines
		for _, b := range cr.remaining {
			message.WriteByte(b)
			
			switch b {
			case '\n':
				// Send the complete line
				select {
				case rawLines <- message:
					message = pool.BytesBuffer.Get().(*bytes.Buffer)
					warnedAboutLongLine = false
				case <-ctx.Done():
					return ctx.Err()
				}
			default:
				// Check line length limit
				if message.Len() >= maxLineLength {
					if !warnedAboutLongLine {
						serverMessages <- dlog.Common.Warn(filePath,
							"Long log line, splitting into multiple lines") + "\n"
						warnedAboutLongLine = true
					}
					message.WriteByte('\n')
					select {
					case rawLines <- message:
						message = pool.BytesBuffer.Get().(*bytes.Buffer)
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
		
		// Clear the remaining buffer - any partial line is now in the message buffer
		cr.remaining = nil
	}
}