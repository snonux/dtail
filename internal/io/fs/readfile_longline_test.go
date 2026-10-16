package fs

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

// TestStartReadsLinesLongerThanScannerBuffer covers snapshot reads of lines
// longer than the pooled 1 MiB scanner buffer: below MaxLineLength such a line
// arrives whole, above it the reader splits it at MaxLineLength, and in both
// cases the read goes on with the next line instead of failing with
// bufio.Scanner's ErrTooLong.
func TestStartReadsLinesLongerThanScannerBuffer(t *testing.T) {
	const mib = 1024 * 1024
	long := strings.Repeat("x", mib+mib/2)

	tests := []struct {
		name          string
		maxLineLength int
		want          []string
	}{
		{
			name:          "line below MaxLineLength above 1 MiB arrives whole",
			maxLineLength: 4 * mib,
			want:          []string{"a\n", long + "\n", "b\n"},
		},
		{
			name:          "line of exactly MaxLineLength arrives whole",
			maxLineLength: len(long),
			want:          []string{"a\n", long + "\n", "b\n"},
		},
		{
			name:          "line above MaxLineLength above 1 MiB is split",
			maxLineLength: mib + mib/4,
			want:          []string{"a\n", long[:mib+mib/4], long[mib+mib/4:] + "\n", "b\n"},
		},
		{
			name:          "default MaxLineLength splits at 1 MiB",
			maxLineLength: 0,
			want:          []string{"a\n", long[:mib], long[mib:] + "\n", "b\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := writeProcessorTestFile(t, "a\n"+long+"\nb\n")
			cat := newSnapshotReadFile(filePath, "glob-id", make(chan string, 10), tt.maxLineLength, testLogger)
			processor := &captureProcessor{}

			if err := cat.Start(context.Background(), lcontext.LContext{}, processor, regex.NewNoop()); err != nil {
				t.Fatalf("reader start failed: %v", err)
			}
			assertLinesEqual(t, processor.lines, tt.want)
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
