package fs

import (
	"io"
	"os"
)

// ByteCounter learns how far a snapshot read got through its file, for the
// read progress a session reports to its client (protocol.Progress).
// Implementations must be safe for concurrent use, as the reader's goroutine
// calls them while the session's progress reporter reads the totals.
type ByteCounter interface {
	// Opened is called at every open of the file, with the open file: the
	// bytes counted for the file start over, as the read starts over. The
	// counter may Stat file while the read runs, to follow a file that grows
	// while it is read (a log still written to), as the read goes on to the
	// end it finds. It must not read, seek or close it; Stat of a file the
	// reader closed meanwhile fails harmlessly with os.ErrClosed.
	Opened(file *os.File)
	// Add counts n more bytes read of the file.
	Add(n int64)
}

// countingReader counts the bytes read through it into a ByteCounter. It sits
// right on the open file, below the line buffer and any decompressor, so it
// counts the bytes on disk once per buffer fill, not per line.
type countingReader struct {
	source  io.Reader
	counter ByteCounter
}

func (r countingReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if n > 0 {
		r.counter.Add(int64(n))
	}
	return n, err
}

// countedSource returns what the line reader reads fd through: fd itself, or,
// with a ByteCounter set, a countingReader over it, after telling the counter
// about the file just opened.
func (f *ReadFile) countedSource(fd *os.File) io.Reader {
	if f.progress == nil {
		return fd
	}
	f.progress.Opened(fd)
	return countingReader{source: fd, counter: f.progress}
}
