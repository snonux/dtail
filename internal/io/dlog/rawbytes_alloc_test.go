package dlog

import (
	"os"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/clients/clientlog"
	"github.com/mimecast/dtail/internal/io/dlog/loggers"
)

var _ clientlog.Logger = (*DLog)(nil)

// newDiscardingSink builds a production sink through loggers.Factory while
// os.Stdout points at the null device, so its stdout writer discards output.
// The sink only reads os.Stdout at construction; the original is restored
// before the test body runs. The test must not be parallel.
func newDiscardingSink(t *testing.T, loggerName string) loggers.Logger {
	t.Helper()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	original := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = original }()

	// A unique source name keeps the Factory singleton private to this test.
	sink, err := loggers.Factory("rawbytes-alloc-"+t.Name(), loggerName,
		loggers.Strategy{FileBase: "rawbytes-alloc"}, loggers.Options{LogDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Factory(%q): %v", loggerName, err)
	}
	return sink
}

// TestClientPayloadRawBytesChainDoesNotAllocate guards the production payload
// chain used by client handlers, clientlog.RawBytes -> DLog.RawBytes -> sink,
// for the stdout sink and the default fout sink without --log-payload. A
// per-line string conversion reintroduced anywhere on that chain shows up here
// as an allocation, which a test against a no-op logger would not notice. The
// messages include one larger than the stdout bufio buffer so the flush to the
// underlying writer is exercised as well.
func TestClientPayloadRawBytesChainDoesNotAllocate(t *testing.T) {
	messages := [][]byte{
		[]byte("2026-01-01T00:00:00Z INFO request=000000001 status=200\n"),
		[]byte("REMOTE|host|100|1|src|line\n"),
		[]byte(strings.Repeat("x", 70*1024) + "\n"),
	}

	for _, loggerName := range []string{"stdout", "fout"} {
		t.Run(loggerName, func(t *testing.T) {
			sink := newDiscardingSink(t, loggerName)
			if _, ok := sink.(loggers.RawBytesWriter); !ok {
				t.Fatalf("%s sink lost the RawBytes capability", loggerName)
			}
			d := &DLog{logger: sink}
			t.Cleanup(d.Flush)

			write := func() {
				for _, message := range messages {
					clientlog.RawBytes(d, message)
				}
			}
			write()

			if allocs := testing.AllocsPerRun(100, write); allocs != 0 {
				t.Fatalf("payload chain through %s allocated %.1f times per run, want 0", loggerName, allocs)
			}
		})
	}
}
