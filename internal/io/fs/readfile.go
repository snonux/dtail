package fs

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
)

type readStatus int

const (
	nothing              readStatus = iota
	abortReading         readStatus = iota
	continueReading      readStatus = iota
	defaultMaxLineLength            = 1024 * 1024
)

// Used to tail and filter a local log file.
type readFile struct {
	// Various statistics (e.g. regex hit percentage, transfer percentage).
	stats
	// Path of log file to tail.
	filePath string
	// Rooted target used for validated server-side re-opens.
	validatedTarget *ValidatedReadTarget
	// The glob identifier of the file.
	globID string
	// Channel to send a server message to the dtail client
	serverMessages chan<- string
	// Periodically retry reading file.
	retry bool
	// Can I skip messages when there are too many?
	canSkipLines bool
	// Seek to the EOF before processing file?
	seekEOF bool
	// Warned already about a long line.
	warnedAboutLongLine bool
	// Maximum line length before a line is split.
	maxLineLength int
}

// String returns the string representation of the readFile
func (f readFile) String() string {
	return fmt.Sprintf(
		"readFile(filePath:%s,globID:%s,retry:%v,canSkipLines:%v,seekEOF:%v)",
		f.filePath,
		f.globID,
		f.retry,
		f.canSkipLines,
		f.seekEOF)
}

// FilePath returns the full file path.
func (f readFile) FilePath() string {
	return f.filePath
}

// Retry reading the file on error?
func (f readFile) Retry() bool {
	return f.retry
}

func (f *readFile) lineLimit() int {
	if f.maxLineLength <= 0 {
		return defaultMaxLineLength
	}
	return f.maxLineLength
}

func (f *readFile) warnAboutLongLine(ctx context.Context) bool {
	if f.warnedAboutLongLine {
		return true
	}

	if f.serverMessages == nil {
		f.warnedAboutLongLine = true
		return true
	}

	select {
	case f.serverMessages <- dlog.Common.Warn(f.filePath,
		"Long log line, splitting into multiple lines") + "\n":
		f.warnedAboutLongLine = true
		return true
	case <-ctx.Done():
		return false
	}
}

func (f *readFile) makeReader() (*bufio.Reader, *os.File, io.Closer, error) {
	if f.filePath == "" && f.globID == "-" {
		return f.makePipeReader()
	}
	return f.makeFileReader()
}

func (f *readFile) makeFileReader() (reader *bufio.Reader, fd *os.File, decompressor io.Closer, err error) {
	if fd, err = f.openFile(); err != nil {
		return
	}

	if f.seekEOF {
		if _, err = fd.Seek(0, io.SeekEnd); err != nil {
			return
		}
	}

	reader, decompressor, err = f.makeCompressedFileReader(fd)
	return
}

func (f *readFile) openFile() (*os.File, error) {
	if f.validatedTarget != nil {
		return f.validatedTarget.Open()
	}
	return os.Open(f.filePath)
}

func (f *readFile) makePipeReader() (*bufio.Reader, *os.File, io.Closer, error) {
	return bufio.NewReader(os.Stdin), nil, nil, nil
}

func (f *readFile) periodicTruncateCheck(ctx context.Context, truncate chan<- struct{}) {
	ticker := time.NewTicker(time.Second * 3)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			select {
			case truncate <- struct{}{}:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (f *readFile) makeCompressedFileReader(fd *os.File) (reader *bufio.Reader, decompressor io.Closer, err error) {
	switch {
	case strings.HasSuffix(f.FilePath(), ".gz"):
		fallthrough
	case strings.HasSuffix(f.FilePath(), ".gzip"):
		dlog.Common.Info(f.FilePath(), "Detected gzip compression format")
		var gzipReader *gzip.Reader
		gzipReader, err = gzip.NewReader(fd)
		if err != nil {
			return
		}
		decompressor = gzipReader
		reader = bufio.NewReader(gzipReader)
	case strings.HasSuffix(f.FilePath(), ".zst"):
		return f.makeZstdReader(fd)
	default:
		reader = bufio.NewReader(fd)
	}
	return
}

// Check wether log file is truncated. Returns nil if not.
func (f *readFile) truncated(fd *os.File) (bool, error) {
	if fd == nil {
		return false, nil
	}

	dlog.Common.Debug(f.filePath, "File truncation check")

	// Can not seek currently open FD.
	currentPosition, err := fd.Seek(0, io.SeekCurrent)
	if err != nil {
		return true, err
	}
	// Can not open file at original path.
	pathFd, err := f.openFile()
	if err != nil {
		return true, err
	}
	defer pathFd.Close()

	// Can not seek file at original path.
	pathPosition, err := pathFd.Seek(0, io.SeekEnd)
	if err != nil {
		return true, err
	}
	if currentPosition > pathPosition {
		return true, errors.New("File got truncated")
	}
	return false, nil
}

