package loggers

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

// withTempLogDir points config.Common.LogDir at a fresh temp dir for the
// duration of a test and restores the previous config afterwards. The file
// logger resolves its output path from config.Common.LogDir at write time.
func withTempLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := config.Common
	config.Common = &config.CommonConfig{LogDir: dir}
	t.Cleanup(func() { config.Common = prev })
	return dir
}

// startFileLogger starts f and returns a stop func that cancels the context and
// JOINS the logger goroutine (wg.Wait). Tests must defer stop() so the goroutine
// has fully exited before returning: withTempLogDir's t.Cleanup restores the
// global config.Common, and a still-running goroutine reading config.Common.LogDir
// would otherwise race that restore. For the same reason none of these tests may
// call t.Parallel — they mutate the process-global config.Common.
func startFileLogger(t *testing.T, f *file) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	f.Start(ctx, &wg)
	return func() {
		cancel()
		wg.Wait()
	}
}

func readLogFile(t *testing.T, dir, base string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, base+".log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("reading log file: %v", err)
	}
	return string(data)
}

// TestFileLoggerNothingLostOnClose verifies every logged line reaches disk when
// the context is cancelled (clean shutdown): the goroutine drains the buffer
// channel and flushes the 64KB writer before closing the fd.
func TestFileLoggerNothingLostOnClose(t *testing.T) {
	dir := withTempLogDir(t)
	base := "close-test"
	f := newFile(Strategy{Rotation: SignalRotation, FileBase: base})
	stop := startFileLogger(t, f)

	const n = 500
	var want strings.Builder
	for i := 0; i < n; i++ {
		line := "line-" + strconv.Itoa(i)
		f.Log(time.Now(), line)
		want.WriteString(line + "\n")
	}

	// stop() cancels the context and joins the goroutine, which flushes and
	// closes the fd on the way out — so all output must be on disk afterwards.
	stop()

	if got := readLogFile(t, dir, base); got != want.String() {
		t.Fatalf("lost output on close: got %d bytes, want %d bytes",
			len(got), want.Len())
	}
}

// TestFileLoggerIdleFlush verifies a single low-volume line (follow/tail style)
// is not stuck behind the 64KB buffer: the idle ticker flushes it to disk
// promptly without any explicit Flush or shutdown. The logger goroutine is
// joined via stop() before returning so it cannot outlive config.Common.
func TestFileLoggerIdleFlush(t *testing.T) {
	dir := withTempLogDir(t)
	base := "idle-test"
	f := newFile(Strategy{Rotation: SignalRotation, FileBase: base})
	stop := startFileLogger(t, f)
	defer stop()

	f.Log(time.Now(), "follow-line")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(readLogFile(t, dir, base), "follow-line") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("follow-style line stuck behind buffer; idle flush did not emit it")
}

// TestFileLoggerExplicitFlush verifies Flush() is SYNCHRONOUS: once it returns,
// the buffered data is already on disk (no polling needed). This is the property
// dlog.FatalPanic relies on to not drop Fatal diagnostics before panicking.
func TestFileLoggerExplicitFlush(t *testing.T) {
	dir := withTempLogDir(t)
	base := "flush-test"
	f := newFile(Strategy{Rotation: SignalRotation, FileBase: base})
	stop := startFileLogger(t, f)
	defer stop()

	f.Log(time.Now(), "flush-me")
	f.Flush()

	if got := readLogFile(t, dir, base); !strings.Contains(got, "flush-me") {
		t.Fatalf("synchronous Flush() did not persist data before returning; got %q", got)
	}
}

// TestFileLoggerRotateDoesNotBlockWithoutWrites verifies that Rotate() does
// not deadlock when no log messages have been produced. Previously rotateCh
// was unbuffered and only drained opportunistically from write(), so a SIGHUP
// before any Log() call would block the caller forever.
func TestFileLoggerRotateDoesNotBlockWithoutWrites(t *testing.T) {
	f := newFile(Strategy{Rotation: SignalRotation, FileBase: "unit-test"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	f.Start(ctx, &wg)

	done := make(chan struct{})
	go func() {
		f.Rotate()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Rotate() blocked without any writes; expected prompt return")
	}

	cancel()
	wg.Wait()
}

// TestFileLoggerCancelBeforeFirstWriteDoesNotPanic verifies that cancelling
// the context before any write has happened does not panic. Previously the
// goroutine called f.fd.Close() unconditionally, but f.fd is only populated
// by the first getWriter() call, so a ctx cancel with no prior writes
// panicked on a nil pointer.
func TestFileLoggerCancelBeforeFirstWriteDoesNotPanic(t *testing.T) {
	f := newFile(Strategy{Rotation: SignalRotation, FileBase: "unit-test"})

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	f.Start(ctx, &wg)

	cancel()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(1 * time.Second):
		t.Fatal("file logger goroutine did not exit after ctx cancel")
	}
}
