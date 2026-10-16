package fs

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

const mib = 1024 * 1024

// longLineCase is a snapshot read of content with maxLineLength (0 for the
// default) and the lines and long-line warnings it must produce.
type longLineCase struct {
	name          string
	content       string
	maxLineLength int
	want          []string
	// wantWarnings is how many long-line warnings the reader sends: none
	// when every line arrives whole, one for a file with split lines (the
	// reader warns once per sequence of split chunks).
	wantWarnings int
}

// longLineCases covers lines longer than the pooled 1 MiB scanner buffer:
// below or at MaxLineLength such a line arrives whole, above it the reader
// splits it at MaxLineLength, and in every case the read goes on with the next
// line instead of failing with bufio.Scanner's ErrTooLong. The exact-limit
// cases at 1 and 2 MiB are on bufio.Scanner's doubling sequence from the
// pooled buffer, where the buffer fills at exactly the limit.
func longLineCases() []longLineCase {
	x := func(n int) string { return strings.Repeat("x", n) }
	whole := func(name string, limit int, line string) longLineCase {
		return longLineCase{name: name, content: "a\n" + line + "\nb\n", maxLineLength: limit,
			want: []string{"a\n", line + "\n", "b\n"}}
	}
	split := func(name string, limit, cut int, line string) longLineCase {
		return longLineCase{name: name, content: "a\n" + line + "\nb\n", maxLineLength: limit,
			want: []string{"a\n", line[:cut], line[cut:] + "\n", "b\n"}, wantWarnings: 1}
	}
	final := func(name string, limit int, line string, want ...string) longLineCase {
		c := longLineCase{name: name, content: "a\n" + line, maxLineLength: limit,
			want: append([]string{"a\n"}, want...)}
		if len(want) > 1 {
			c.wantWarnings = 1
		}
		return c
	}
	long := x(mib + mib/2)
	crlf := x(mib+mib/2) + "\r"
	return []longLineCase{
		whole("line below MaxLineLength above 1 MiB arrives whole", 4*mib, long),
		whole("line of exactly MaxLineLength off the doubling sequence", len(long), long),
		whole("line of exactly the default MaxLineLength", 0, x(mib)),
		whole("line of exactly MaxLineLength of 2 MiB", 2*mib, x(2*mib)),
		split("line one byte above MaxLineLength of 2 MiB", 2*mib, 2*mib, x(2*mib+1)),
		split("line above MaxLineLength above 1 MiB is split", mib+mib/4, mib+mib/4, long),
		split("default MaxLineLength splits at 1 MiB", 0, mib, long),
		whole("CRLF line below MaxLineLength above 1 MiB arrives whole", 4*mib, crlf),
		// The carriage return is part of the line before its newline.
		whole("CRLF line of exactly MaxLineLength of 2 MiB", 2*mib, x(2*mib-1)+"\r"),
		final("final line without newline below MaxLineLength", 4*mib, long, long),
		final("final line without newline of exactly MaxLineLength", 2*mib, x(2*mib), x(2*mib)),
		final("final line without newline above MaxLineLength", mib+mib/4, long,
			long[:mib+mib/4], long[mib+mib/4:]),
	}
}

func TestStartReadsLinesLongerThanScannerBuffer(t *testing.T) {
	for _, tt := range longLineCases() {
		t.Run(tt.name, func(t *testing.T) {
			path := writeProcessorTestFile(t, tt.content)
			assertLongLineRead(t, path, tt)
		})
	}
}

// TestStartReadsLongLinesOfGzipFiles runs the long-line cases through the
// gzip decompressor, whose reader hands the scanner data in other chunks.
func TestStartReadsLongLinesOfGzipFiles(t *testing.T) {
	for _, tt := range longLineCases() {
		t.Run(tt.name, func(t *testing.T) {
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			if _, err := writer.Write([]byte(tt.content)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			assertLongLineRead(t, writeLongLineFile(t, "test.log.gz", compressed.Bytes()), tt)
		})
	}
}

// writeLongLineFile writes content to a file name in a new temporary
// directory and returns its path.
func writeLongLineFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertLongLineRead reads path with a snapshot reader and checks the lines,
// their numbers and the long-line warnings against tt.
func assertLongLineRead(t *testing.T, path string, tt longLineCase) {
	t.Helper()
	messages := make(chan string, 10)
	cat := newSnapshotReadFile(path, "glob-id", messages, tt.maxLineLength, testLogger)
	processor := &captureProcessor{}

	if err := cat.Start(context.Background(), lcontext.LContext{}, processor, regex.NewNoop()); err != nil {
		t.Fatalf("reader start failed: %v", err)
	}
	assertLinesEqual(t, processor.lines, tt.want)
	for i, lineNum := range processor.lineNums {
		if lineNum != uint64(i+1) {
			t.Fatalf("line %d has number %d, want %d", i, lineNum, i+1)
		}
	}
	assertLongLineWarnings(t, messages, tt.wantWarnings)
}

// assertLongLineWarnings checks that messages holds exactly want server
// messages. A reader of these tests sends no other message than the long-line
// warning, whose text comes from the test logger (just its newline here).
func assertLongLineWarnings(t *testing.T, messages chan string, want int) {
	t.Helper()
	close(messages)
	got := 0
	for message := range messages {
		if message != LongLineWarning(testLogger, "") {
			t.Fatalf("unexpected server message %q", message)
		}
		got++
	}
	if got != want {
		t.Fatalf("got %d long-line warnings, want %d", got, want)
	}
}

// chunkReader returns one chunk per Read, so a test decides where the reads of
// a follow reader end, and cancels the read after the last chunk.
type chunkReader struct {
	chunks []string
	cancel context.CancelFunc
}

func (r *chunkReader) Read(p []byte) (int, error) {
	n := copy(p, r.chunks[0])
	if r.chunks[0] = r.chunks[0][n:]; r.chunks[0] == "" {
		r.chunks = r.chunks[1:]
	}
	if len(r.chunks) == 0 {
		r.cancel()
	}
	return n, nil
}

// TestFollowKeepsLinesOfExactlyMaxLineLengthWhole checks that a follow read,
// like a snapshot read, keeps a pending line of exactly MaxLineLength until
// its newline arrives in a later read, and only splits a line longer than
// that, warning once.
func TestFollowKeepsLinesOfExactlyMaxLineLengthWhole(t *testing.T) {
	tests := []struct {
		name         string
		chunks       []string
		want         []string
		wantWarnings int
	}{
		{name: "exact limit, newline in the next read", chunks: []string{"abcd", "\nxy\n"},
			want: []string{"abcd", "xy"}},
		{name: "exact limit over several reads", chunks: []string{"ab", "cd", "\n"},
			want: []string{"abcd"}},
		{name: "one byte above the limit", chunks: []string{"abcde", "\nxy\n"},
			want: []string{"abcde", "xy"}, wantWarnings: 1},
		{name: "above the limit over several reads", chunks: []string{"abcd", "e", "fg\n"},
			want: []string{"abcde", "fg"}, wantWarnings: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := make(chan string, 10)
			rf := ReadFile{logger: testLogger, filePath: "follow.log", globID: "glob-id",
				serverMessages: messages, follow: true, maxLineLength: 4}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := &chunkReader{chunks: tt.chunks, cancel: cancel}
			processor := &captureProcessor{}

			// fd is nil: the reads never return io.EOF, so the reader never
			// inspects the file, and the context ends it after the last chunk.
			if err := rf.tailWithProcessorOptimized(ctx, nil, bufio.NewReader(reader), make(chan struct{}),
				rf.newFilteringProcessor(lcontext.LContext{}, processor, regex.NewNoop())); err != nil {
				t.Fatalf("tail returned error: %v", err)
			}
			assertLinesEqual(t, processor.lines, tt.want)
			assertLongLineWarnings(t, messages, tt.wantWarnings)
		})
	}
}

// TestScannerTokenLimit checks that the snapshot scanner may always hold a
// line of the configured limit plus its newline, never less than the pooled
// buffer, and does not overflow for the largest int.
func TestScannerTokenLimit(t *testing.T) {
	tests := []struct {
		maxLineLength int
		want          int
	}{
		{maxLineLength: 0, want: defaultMaxLineLength + 1},
		{maxLineLength: -1, want: defaultMaxLineLength + 1},
		{maxLineLength: 1024, want: minReadScannerTokenSize},
		{maxLineLength: minReadScannerTokenSize - 1, want: minReadScannerTokenSize},
		{maxLineLength: minReadScannerTokenSize, want: minReadScannerTokenSize + 1},
		{maxLineLength: 4 * minReadScannerTokenSize, want: 4*minReadScannerTokenSize + 1},
		{maxLineLength: math.MaxInt, want: math.MaxInt},
	}

	for _, tt := range tests {
		f := ReadFile{maxLineLength: tt.maxLineLength}
		if got := f.scannerTokenLimit(); got != tt.want {
			t.Errorf("scannerTokenLimit() with MaxLineLength %d = %d, want %d",
				tt.maxLineLength, got, tt.want)
		}
	}
}

// assertLinesEqual compares line slices by length and content, without
// printing the multi-MiB lines of a mismatch.
func assertLinesEqual(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d lines (lengths %v), want %d (lengths %v)",
			len(got), lineLengths(got), len(want), lineLengths(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d differs: got length %d, want length %d", i, len(got[i]), len(want[i]))
		}
	}
}

func lineLengths(lines []string) []int {
	lengths := make([]int, len(lines))
	for i, l := range lines {
		lengths[i] = len(l)
	}
	return lengths
}
