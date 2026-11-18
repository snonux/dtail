package fs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/mimecast/dtail/internal/io/pool"
)

// Bounded sample reads: a few recent lines of a file for the MapReduce field
// probe (internal/mapr/probe), read once, never followed, with bounded memory
// and I/O whatever the size of the file.

const (
	// DefaultSampleWindowBytes is the default of SampleOptions.WindowBytes:
	// the tail sample of an uncompressed file reads the last 256 KiB of it.
	DefaultSampleWindowBytes = 256 * 1024
	// DefaultSampleMaxLines is the default of SampleOptions.MaxLines.
	DefaultSampleMaxLines = 500
	// SampleHeadScanFactor bounds the work of a head sample (SampleHead, the
	// sample of a compressed file): it stops once it scanned this many times
	// WindowBytes of input, terminators and skipped lines included, whatever
	// it kept. Skipped oversized lines therefore can not make it decompress
	// a whole multi-GB file. A head of normal lines (at most WindowBytes of
	// line data plus a terminator each) stays far below the bound.
	SampleHeadScanFactor = 4
)

// SampleOptions bounds a sample read. Zero or negative values select the
// defaults.
type SampleOptions struct {
	// WindowBytes is the size of the tail window of an uncompressed file and
	// the most line data, without line terminators, Sample.Lines hold (a line
	// larger than that on its own is skipped). Sample.FirstLine comes on top.
	// A head sample (compressed file) scans at most SampleHeadScanFactor
	// times WindowBytes of input, plus the rest of the line or piece it is in
	// (up to MaxLineLength).
	WindowBytes int64
	// MaxLines is the most lines a sample holds.
	MaxLines int
	// MaxLineLength is Server.MaxLineLength: a longer line is cut into pieces
	// of this length with SplitLine, as every other read cuts it, and each
	// piece counts as a line of its own.
	MaxLineLength int
}

// Sample is the result of a sample read.
type Sample struct {
	// Lines are the sampled lines, oldest first, without their LF or CRLF
	// terminator (a bare CR stays, as the whitespace-preserving MapReduce
	// parsers expect).
	Lines []string
	// FirstLine is the first line of the file (without terminator, cut at
	// MaxLineLength and at WindowBytes) when Lines do not start with it, i.e.
	// a tail sample whose window starts past offset 0 or whose limits
	// evicted the first line of a window starting at 0. Formats whose first
	// line is a header (csv) need it to name the fields of Lines: the caller
	// puts it in front of Lines for them. It does not count towards the
	// limits of Lines.
	FirstLine string
	// HasFirstLine tells that FirstLine is set (it may be empty).
	HasFirstLine bool
}

// SampleCollector gathers the lines of a sample within the limits of its
// SampleOptions, keeping either the first or the last lines it is given.
// Lines are stored without their LF or CRLF terminator (see Sample.Lines).
type SampleCollector struct {
	maxLines int
	maxBytes int64
	keepLast bool
	lines    []string
	// head is the index of the first kept line of lines when keepLast is set:
	// lines before it were evicted and are compacted away now and then.
	head  int
	bytes int64
	// evicted is set once a line was evicted, i.e. Lines no longer start
	// with the first line added.
	evicted bool
}

// NewSampleCollector returns a collector bounded by options (defaults filled
// in). With keepLast it keeps the most recent lines and evicts older ones
// when a limit is exceeded; otherwise it keeps the first lines and reports
// itself full once the next line would not fit any more. Either way it skips
// a line larger than the byte limit on its own (see Add).
func NewSampleCollector(options SampleOptions, keepLast bool) *SampleCollector {
	options = options.WithDefaults()
	return &SampleCollector{
		maxLines: options.MaxLines,
		maxBytes: options.WindowBytes,
		keepLast: keepLast,
	}
}

// Add adds a copy of line. A line larger than the byte limit on its own is
// skipped: it never fits, and it must neither evict the lines kept so far
// nor end the sample, so the lines around it are still sampled. Add returns
// false when the collector keeps the first lines and is full (the line count
// is reached or the line does not fit into the bytes left), so that the
// caller can stop reading: line was not added then. A collector keeping the
// last lines always returns true and evicts its oldest lines to make room.
func (c *SampleCollector) Add(line []byte) bool {
	line = trimLineTerminator(line)
	size := int64(len(line))
	if size > c.maxBytes {
		return true
	}
	if !c.keepLast {
		if len(c.lines) >= c.maxLines || c.bytes+size > c.maxBytes {
			return false
		}
		c.lines = append(c.lines, string(line))
		c.bytes += size
		return true
	}
	c.lines = append(c.lines, string(line))
	c.bytes += size
	c.evict()
	return true
}

// Lines returns the collected lines, oldest first.
func (c *SampleCollector) Lines() []string {
	return append([]string(nil), c.lines[c.head:]...)
}

// WithDefaults returns o with the defaults filled in for unset limits.
func (o SampleOptions) WithDefaults() SampleOptions {
	if o.WindowBytes <= 0 {
		o.WindowBytes = DefaultSampleWindowBytes
	}
	if o.MaxLines <= 0 {
		o.MaxLines = DefaultSampleMaxLines
	}
	return o
}

// SampleFile samples the file of target, which must be a FileKind target. An
// uncompressed file is sampled at its end (SampleTail); a gzip or zstd file,
// chosen by the suffix of path as for a read command (CompressionFormat), at
// its beginning (SampleHead), since a compressed stream can not be read from
// its end. The caller checks the read permissions before; SampleFile only
// opens the target through its rooted, validated path.
func SampleFile(ctx context.Context, target ValidatedReadTarget, path string,
	options SampleOptions) (Sample, error) {

	if err := ctx.Err(); err != nil {
		return Sample{}, err
	}
	fd, err := target.Open()
	if err != nil {
		return Sample{}, err
	}
	defer func() { _ = fd.Close() }()

	switch format := CompressionFormat(path); format {
	case "":
		return SampleTail(ctx, fd, options)
	case "gzip":
		decompressor := &lazyGzipReader{source: fd}
		defer func() { _ = decompressor.Close() }()
		return SampleHead(ctx, decompressor, options)
	default:
		decompressor, err := newZstdDecompressor(path, fd)
		if err != nil {
			return Sample{}, err
		}
		defer func() { _ = decompressor.Close() }()
		return SampleHead(ctx, decompressor, options)
	}
}

// SampleTail samples the last lines of the uncompressed file: those that
// start within its last options.WindowBytes bytes, at most options.MaxLines
// of them. The first line of the window is dropped unless the window starts
// at the beginning of the file or of a line, since it is a partial one. A
// last line without a newline is kept, as a cat read keeps it. A line longer
// than the window therefore yields no line at all. When the window starts
// past offset 0, or the limits evicted the file's first line from the lines
// kept, FirstLine holds the first line of the file (see Sample).
// The file is read with ReadAt, so its offset does not change.
func SampleTail(ctx context.Context, file *os.File, options SampleOptions) (Sample, error) {
	options = options.WithDefaults()
	if err := ctx.Err(); err != nil {
		return Sample{}, err
	}
	window, start, err := readTailWindow(file, options.WindowBytes)
	if err != nil {
		return Sample{}, err
	}

	var sample Sample
	limit := LineLimit(options.MaxLineLength)
	if start > 0 {
		if sample.FirstLine, err = readFirstLine(file, options.WindowBytes, limit); err != nil {
			return Sample{}, err
		}
		sample.HasFirstLine = true
	}
	collector := NewSampleCollector(options, true)
	var firstToken []byte
	for first := true; len(window) > 0; first = false {
		if err := ctx.Err(); err != nil {
			return Sample{}, err
		}
		advance, token, _ := SplitLine(window, true, limit)
		if first {
			firstToken = token
		}
		collector.Add(token)
		window = window[advance:]
	}
	sample.Lines = collector.Lines()
	// A window starting at offset 0 holds the first line, but the limits
	// may have evicted it (a file of more than MaxLines short lines): it is
	// returned separately then too, as the window held it whole or cut at
	// MaxLineLength like readFirstLine cuts it.
	if start == 0 && collector.evicted {
		sample.FirstLine = string(trimLineTerminator(firstToken))
		sample.HasFirstLine = true
	}
	return sample, nil
}

// SampleHead samples the first lines read from reader, at most
// options.MaxLines of them and at most options.WindowBytes of line data. It
// stops reading once the sample is full, ctx is done or it scanned
// SampleHeadScanFactor times options.WindowBytes of input (see
// SampleHeadScanFactor; the line or piece that crosses the bound is the
// last one scanned). Its lines start with the first line of the input, so
// HasFirstLine is never set.
func SampleHead(ctx context.Context, reader io.Reader, options SampleOptions) (Sample, error) {
	options = options.WithDefaults()
	limit := LineLimit(options.MaxLineLength)
	scanner := bufio.NewScanner(reader)
	bufPtr := pool.GetScannerBuffer()
	defer pool.PutScannerBuffer(bufPtr)
	scanner.Buffer(*bufPtr, ScannerTokenLimit(limit))
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, _ := SplitLine(data, atEOF, limit)
		return advance, token, nil
	})

	collector := NewSampleCollector(options, false)
	scanBound := headScanBound(options.WindowBytes)
	var scanned int64
	for scanned < scanBound {
		if err := ctx.Err(); err != nil {
			return Sample{}, err
		}
		if !scanner.Scan() || !collector.Add(scanner.Bytes()) {
			break
		}
		scanned += int64(len(scanner.Bytes()))
	}
	if err := scanner.Err(); err != nil {
		return Sample{}, fmt.Errorf("read sample: %w", err)
	}
	return Sample{Lines: collector.Lines()}, nil
}

// evict drops the oldest kept lines while a limit is exceeded, and compacts
// the slice once the evicted lines outnumber the kept ones, so that its
// backing array stays proportional to the limits.
func (c *SampleCollector) evict() {
	for c.head < len(c.lines) && (len(c.lines)-c.head > c.maxLines || c.bytes > c.maxBytes) {
		c.bytes -= int64(len(c.lines[c.head]))
		c.lines[c.head] = ""
		c.head++
		c.evicted = true
	}
	if c.head > c.maxLines {
		c.lines = append(c.lines[:0], c.lines[c.head:]...)
		c.head = 0
	}
}

// readTailWindow reads the last window bytes of file, starting at the
// beginning of a line, and returns them with the offset the window starts
// at: it reads one byte before the window too, and drops everything up to
// and including the first newline unless the window starts at offset 0. A
// file that shrinks meanwhile yields what is still there.
func readTailWindow(file *os.File, window int64) ([]byte, int64, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat sample file: %w", err)
	}
	start := max(0, info.Size()-window)
	readFrom := max(0, start-1)
	data := make([]byte, info.Size()-readFrom)
	n, err := file.ReadAt(data, readFrom)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, fmt.Errorf("read sample window: %w", err)
	}
	data = data[:n]
	if start == 0 {
		return data, 0, nil
	}
	newline := bytes.IndexByte(data, '\n')
	if newline < 0 {
		return nil, start, nil
	}
	return data[newline+1:], start, nil
}

// readFirstLine returns the first line of file without its terminator, read
// from at most window bytes at offset 0 and cut at limit bytes, like the
// first piece of a line cut with SplitLine. A first line longer than the
// window is cut at the window.
func readFirstLine(file *os.File, window int64, limit int) (string, error) {
	size := window
	if int64(limit) < window {
		// One byte more, to see whether a line of exactly limit bytes ends
		// there; limit < window rules out an overflow (math.MaxInt limit).
		size = int64(limit) + 1
	}
	data := make([]byte, size)
	n, err := file.ReadAt(data, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read first line of sample file: %w", err)
	}
	_, token, _ := SplitLine(data[:n], true, limit)
	return string(trimLineTerminator(token)), nil
}

// headScanBound returns how much input a head sample with the window scans
// at most: SampleHeadScanFactor times the window, saturated at MaxInt64.
func headScanBound(window int64) int64 {
	if window > math.MaxInt64/SampleHeadScanFactor {
		return math.MaxInt64
	}
	return SampleHeadScanFactor * window
}

// trimLineTerminator removes a trailing LF or CRLF from line.
func trimLineTerminator(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
	}
	return line
}
