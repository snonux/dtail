//go:build linux

package journal

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	journaltest "github.com/mimecast/dtail/internal/io/journal/testhelper"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

// chunkReader returns at most size bytes per Read, so that a long stderr line
// reaches the scanner over many reads, as from a pipe.
type chunkReader struct {
	data string
	size int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.size, len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

// TestScanJournalStderrTruncatesLongLines checks that a stderr line above
// maxStderrTokenSize is forwarded cut to that length with a note, its rest
// dropped, and the scan goes on with the next lines, instead of failing with
// bufio.ErrTooLong (which cancelled the journalctl run).
func TestScanJournalStderrTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("e", 3*maxStderrTokenSize+7)
	exact := strings.Repeat("f", maxStderrTokenSize)
	unterminated := strings.Repeat("g", 2*maxStderrTokenSize)
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "LongLineBetweenShortOnes",
			input: "first\n" + long + "\nafter\r\n" + exact + "\ntail",
			want: []string{"first", long[:maxStderrTokenSize] + stderrTruncatedNote,
				"after", exact, "tail"},
		},
		{
			name:  "LongLineAtEOFWithoutNewline",
			input: "first\n" + unterminated,
			want:  []string{"first", unterminated[:maxStderrTokenSize] + stderrTruncatedNote},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			err := scanJournalStderr(context.Background(), &chunkReader{data: tc.input, size: 64 * 1024},
				func(message string) { got = append(got, message) })
			if err != nil {
				t.Fatalf("scan stderr: %v", err)
			}
			want := make([]string, len(tc.want))
			for i, line := range tc.want {
				want[i] = "journalctl stderr: " + line + "\n"
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("stderr messages: got %s, want %s", describeLines(got), describeLines(want))
			}
		})
	}
}

// TestStartKeepsReadingAfterLongStderrLine checks the whole reader: a
// journalctl stderr line above maxStderrTokenSize no longer ends the read
// with "scan journalctl stderr: bufio.Scanner: token too long".
func TestStartKeepsReadingAfterLongStderrLine(t *testing.T) {
	long := strings.Repeat("e", 2*maxStderrTokenSize)
	journaltest.InstallMock(t, journaltest.Scenario{
		Default: journaltest.Invocation{
			Lines:  []string{"alpha", "beta"},
			Stderr: []string{long, "after"},
		},
	})
	messages := make(chan string, 4)
	reader, err := NewReader(ReaderOptions{SourceID: "journal-id", ServerMessages: messages})
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	processor := &captureProcessor{}
	if err := reader.Start(context.Background(), lcontext.LContext{}, processor, regex.NewNoop()); err != nil {
		t.Fatalf("start reader: %v", err)
	}
	if want := []string{"alpha\n", "beta\n"}; !reflect.DeepEqual(processor.lines, want) {
		t.Fatalf("lines = %q, want %q", processor.lines, want)
	}
	close(messages)
	var got []string
	for message := range messages {
		got = append(got, message)
	}
	want := []string{
		"journalctl stderr: " + long[:maxStderrTokenSize] + stderrTruncatedNote + "\n",
		"journalctl stderr: after\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stderr messages: got %s, want %s", describeLines(got), describeLines(want))
	}
}
