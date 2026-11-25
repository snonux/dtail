package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

// newPostRecorder returns a readProgress whose posts are recorded, decoded
// from their generation tag, and the recorded posts.
func newPostRecorder(t *testing.T) (*readProgress, *[]string) {
	t.Helper()
	var posts []string
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	progress := newReadProgress(func(message string) {
		_, decoded := decodeGeneratedMessage(message)
		posts = append(posts, decoded)
	}, done)
	return progress, &posts
}

func snapshotOf(p *readProgress) protocol.Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked()
}

// TestReadProgressCountsEndedFilesByWhatWasRead checks that a file whose read
// ended is done in full with the bytes read as its size: bytes stays what was
// read, and the rest of a file whose read ended early leaves the total.
func TestReadProgressCountsEndedFilesByWhatWasRead(t *testing.T) {
	progress, posts := newPostRecorder(t)
	first := progress.register(0, 100)
	second := progress.register(0, 50)
	first.Add(40)
	if got, want := snapshotOf(progress), (protocol.Progress{Bytes: 40, Total: 150, FilesTotal: 2}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// A read ended early (--max, an error): its 60 unread bytes are neither
	// read nor to be read any more.
	progress.finish(first)
	second.Add(70) // read past its size: the file grew
	progress.flush()
	progress.flush() // unchanged: not posted again
	want := protocol.EncodeProgress(protocol.Progress{Bytes: 110, Total: 110, Files: 1, FilesTotal: 2})
	if len(*posts) != 1 || (*posts)[0] != want {
		t.Fatalf("posts = %q, want [%q]", *posts, want)
	}
}

func TestReadProgressStartsOverForANewGeneration(t *testing.T) {
	progress, _ := newPostRecorder(t)
	progress.register(1, 100).Add(100)
	progress.register(2, 10)
	stale := progress.register(1, 1000)
	stale.Add(1000)
	if got, want := snapshotOf(progress), (protocol.Progress{Total: 10, FilesTotal: 1}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestReadProgressReporterEndsWithTheReads checks that the reporting
// goroutine ends once every registered file's read ended, and that a file
// registered later starts it again.
func TestReadProgressReporterEndsWithTheReads(t *testing.T) {
	progress, posts := newPostRecorder(t)
	isStarted := func() bool {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		return progress.started
	}
	file := progress.register(0, 10)
	if !isStarted() {
		t.Fatal("no reporter after a registration")
	}
	if progress.flushAndIdle() {
		t.Fatal("reporter idle while a file is read")
	}
	file.Add(10)
	progress.finish(file)
	if !progress.flushAndIdle() || isStarted() {
		t.Fatal("reporter not idle after the last read ended")
	}
	want := protocol.EncodeProgress(protocol.Progress{Bytes: 10, Total: 10, Files: 1, FilesTotal: 1})
	progress.mu.Lock() // posts are appended under it
	got := (*posts)[len(*posts)-1]
	progress.mu.Unlock()
	if got != want {
		t.Fatalf("last post %q, want %q", got, want)
	}
	progress.register(0, 5)
	if !isStarted() {
		t.Fatal("no reporter for a file registered after the reads ended")
	}
}

// TestReadProgressFollowsAGrowingFile appends to a file while it is "read":
// the total follows its size through the open descriptor.
func TestReadProgressFollowsAGrowingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "growing.log")
	if err := os.WriteFile(path, []byte("0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	progress, _ := newPostRecorder(t)
	file := progress.register(0, 11)
	fd, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	file.Opened(fd)
	file.Add(11)

	appender, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appender.WriteString(strings.Repeat("x", 89) + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = appender.Close()

	if got, want := snapshotOf(progress), (protocol.Progress{Bytes: 11, Total: 101, FilesTotal: 1}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	file.Add(90)
	progress.finish(file)
	if got, want := snapshotOf(progress), (protocol.Progress{Bytes: 101, Total: 101, Files: 1, FilesTotal: 1}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestProgressGoesAheadOfPayload checks that a posted progress frame is
// taken before payload that is still queued, so that a read keeping the
// session output full does not hold it back.
func TestProgressGoesAheadOfPayload(t *testing.T) {
	handler := newSpacedPathTestHandler(t, []string{"readfiles:.*"}, nil)
	handler.EnableDirectOutput()
	if err := handler.EnqueueOutput(context.Background(), 0, []byte("payload\n"), nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	message := protocol.EncodeProgress(protocol.Progress{Bytes: 1, Total: 2, FilesTotal: 1})
	handler.postProgress(message)
	result := handler.next(make([]byte, 4096))
	if result.kind != outputReadServerMessage || result.message != message {
		t.Fatalf("got %+v, want the progress frame first", result)
	}
	if handler.progressQueued() {
		t.Fatal("progress frame still queued after it was taken")
	}
}

// writeProgressTestFiles writes a plain and a gzip log and returns their
// paths and summed size on disk.
func writeProgressTestFiles(t *testing.T) ([]string, int64) {
	t.Helper()
	dir := t.TempDir()
	lines := strings.Repeat(spacedPathTestLine+" some more text of the line\n", 5000)
	plain := filepath.Join(dir, "a.log")
	if err := os.WriteFile(plain, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(lines))
	_ = writer.Close()
	gz := filepath.Join(dir, "b.log.gz")
	if err := os.WriteFile(gz, compressed.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{plain, gz}, int64(len(lines) + compressed.Len())
}

// TestSessionReportsFinalProgressBeforeClose runs dcat's and dgrep's command
// stream in server and serverless mode and checks that the client gets the
// final progress, every byte of both files, ahead of the close handshake.
func TestSessionReportsFinalProgressBeforeClose(t *testing.T) {
	files, size := writeProgressTestFiles(t)
	final := protocol.EncodeProgress(protocol.Progress{Bytes: size, Total: size, Files: 2, FilesTotal: 2})
	for _, mode := range []omode.Mode{omode.CatClient, omode.GrepClient} {
		for _, serverless := range []bool{false, true} {
			t.Run(mode.String()+"/serverless="+strconv.FormatBool(serverless), func(t *testing.T) {
				var output *testOutput
				handler := newSpacedPathTestHandler(t, []string{"readfiles:.*"}, nil)
				if serverless {
					output = &testOutput{}
					handler = newSpacedPathTestHandler(t, []string{"readfiles:.*"}, output)
				}
				transport, _ := readSpecOutput(t, handler, output, mode, files...)
				progressAt := strings.LastIndex(transport, final)
				closeAt := strings.Index(transport, ".syn close connection")
				if progressAt < 0 || closeAt < 0 || progressAt > closeAt {
					t.Fatalf("final progress %q not ahead of the close handshake in %q", final,
						tail(transport, 400))
				}
			})
		}
	}
}

// TestFollowReadsReportNoProgress checks that tail mode reports nothing.
func TestFollowReadsReportNoProgress(t *testing.T) {
	handler := newSpacedPathTestHandler(t, []string{"readfiles:.*"}, nil)
	command := &readCommand{progress: handler.readProgress, mode: omode.TailClient}
	files, _ := writeProgressTestFiles(t)
	target, err := handler.PrepareReadTarget(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if command.trackProgress(&target) != nil {
		t.Fatal("a follow read is tracked")
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
