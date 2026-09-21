// Package readhub shares one follow read of a file between the dserver
// sessions that tail it. One reader per file reads, decompresses and splits
// the file into lines once and fans the lines out to every subscribed session.
// Each session keeps its own regex, local context, line numbering and
// processor, applied by an fs.LineFilter on the session's own goroutine, so a
// session sees the lines it would see from a private follow read, apart from
// where it starts (see Hub.Follow).
package readhub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/regex"
)

// defaultQueueChunks bounds how many chunks, of about chunkSize bytes each, a
// subscriber may have waiting before the shared reader waits for it.
const defaultQueueChunks = 64

// ErrStopped reports that the session's max-count limit ended its read. A
// private follow reader returns from Start at this point and the read command
// starts it again, which re-reads the file from its beginning; a shared read
// cannot do that for one session, so it hands the read back to the caller.
var ErrStopped = errors.New("shared read stopped by the session's max-count limit")

// ErrReaderFailed reports that the shared reader failed for good, so the
// session's read ended. If the reader panicked, the error also wraps
// fs.ErrReaderWorkerPanic, as a private reader's worker panic does.
var ErrReaderFailed = errors.New("shared reader failed")

// Options configures a Hub.
type Options struct {
	// Logger receives the hub's diagnostics.
	Logger logging.Logger
	// MaxLineLength is the line length at which the reader splits a line.
	MaxLineLength int
	// RetryInterval is the pause before the reader opens the file again after
	// a read ended, e.g. because the file was rotated, as the read command's
	// retry interval is for a private reader.
	RetryInterval time.Duration
	// QueueChunks bounds each subscriber's queue; zero selects a default.
	QueueChunks int
	// GroupWait bounds how long a one-shot group read waits for its members
	// to join (see Hub.ReadOnce); zero selects DefaultGroupWait.
	GroupWait time.Duration
	// MaxGroupMembers bounds how many sessions share a one-shot group read;
	// dserver sets it to its cat slots, as every member holds one during the
	// read. Zero means no bound.
	MaxGroupMembers int
	// GroupMemory is how long the hub remembers a one-shot group read that
	// ended; zero selects DefaultGroupMemory.
	GroupMemory time.Duration
}

// Session describes one session's follow read of a file.
type Session struct {
	// Target is the session's own validated target for the file. The shared
	// reader opens the file only through the target of a subscribed session.
	Target fs.ValidatedReadTarget
	// FilePath is the path the session reads, as for a private reader.
	FilePath string
	// GlobID is the source ID the session's processor receives with a line.
	GlobID string
	// LContext, Regex: the session's local context and line regex.
	LContext lcontext.LContext
	Regex    regex.Regex
	// ServerMessages receives the reader's messages for the session, such as
	// the long line warning. It may be nil.
	ServerMessages chan<- string
	// Logger words the session's messages, as the logger of its private
	// reader would; nil selects the hub's logger.
	Logger logging.Logger
	// NewProcessor makes the processor for one read of the file, like the
	// read command does for every iteration of its retry loop. The hub
	// flushes and closes every processor it made.
	NewProcessor func() line.Processor
}

// Hub shares follow reads of the same file between sessions. It is safe for
// concurrent use.
type Hub struct {
	options Options
	logger  logging.Logger
	seams   hubSeams

	mu      sync.Mutex
	entries map[entryKey]*entry
	// oneshot holds the one-shot group reads; see oneshot.go.
	oneshot groupReads
}

// hubSeams are the reader operations an entry performs, replaceable in tests.
type hubSeams struct {
	startReader   func(ctx context.Context, reader *fs.ReadFile, processor line.Processor) error
	replaceTarget func(reader *fs.ReadFile, target fs.ValidatedReadTarget) error
}

func defaultSeams() hubSeams {
	return hubSeams{
		startReader: func(ctx context.Context, reader *fs.ReadFile, processor line.Processor) error {
			return reader.Start(ctx, lcontext.LContext{}, processor, regex.NewNoop())
		},
		replaceTarget: func(reader *fs.ReadFile, target fs.ValidatedReadTarget) error {
			return reader.ReplaceTarget(target)
		},
	}
}

type entryKey struct {
	path        string
	compression string
}

// New returns an empty hub.
func New(options Options) *Hub {
	if options.QueueChunks <= 0 {
		options.QueueChunks = defaultQueueChunks
	}
	return &Hub{
		options: options,
		logger:  logging.OrNop(options.Logger),
		seams:   defaultSeams(),
		entries: make(map[entryKey]*entry),
	}
}

// Follow feeds session with the lines appended to its file from now on, from
// the file's shared reader, which it starts if no other session follows the
// file. It blocks like a private follow read and returns nil once ctx ends,
// ErrStopped when the session's max-count limit ended the read, and a
// processor error or ErrReaderFailed otherwise; ErrReaderFailed together with
// fs.ErrReaderWorkerPanic means the shared reader panicked. After ErrStopped
// or an error, a private reader would start over; the caller decides how to
// continue.
//
// Where a session starts differs from a private follow read, which starts at
// the end of the file when it opens it: a session gets the lines the shared
// reader publishes after the session joined. That includes lines the reader
// read before the join but had not published yet, and, while the reader lags
// behind the end of the file (it waits for a session with a full queue), the
// backlog it has still to read. When the private reader opens the file in the
// middle of a line, it delivers the rest of that line; a shared session gets
// the next whole line. And a trailing line the file's writer has not finished
// yet is not delivered when the session leaves, unlike the private reader,
// which passes such a fragment on when its read is cancelled.
func (h *Hub) Follow(ctx context.Context, session Session) error {
	if err := validateSession(session); err != nil {
		return err
	}
	sub := newSubscriber(session, h.options.QueueChunks)
	e := h.join(sub)
	defer h.leave(e, sub)
	return sub.run(ctx, h.logger)
}

func validateSession(session Session) error {
	switch {
	case session.Target.Kind != fs.FileKind:
		return fmt.Errorf("shared read requires a file target, got kind %d", session.Target.Kind)
	case session.Target.ResolvedPath() == "":
		return errors.New("shared read requires a validated target")
	case session.FilePath == "":
		return errors.New("shared read requires a file path")
	case session.NewProcessor == nil:
		return errors.New("shared read requires a processor factory")
	}
	return nil
}

// join adds sub to the entry for its file, starting one if needed.
func (h *Hub) join(sub *subscriber) *entry {
	key := entryKey{
		path:        sub.session.Target.ResolvedPath(),
		compression: fs.CompressionFormat(sub.session.FilePath),
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entries[key]
	if e == nil {
		e = newEntry(key, sub.session, h.options, h.logger, h.seams, h.forget)
		h.entries[key] = e
		e.add(sub)
		e.start()
		return e
	}
	e.add(sub)
	return e
}

// leave removes sub from e. The last subscriber stops e's reader; otherwise
// a reader using sub's target is handed another subscriber's target.
func (h *Hub) leave(e *entry, sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if remaining := e.remove(sub); remaining == 0 {
		if h.entries[e.key] == e {
			delete(h.entries, e.key)
		}
		e.stop()
	}
}

// forget drops e from the hub when its reader failed for good, so that the
// next session starts a fresh reader instead of joining a dead one.
func (h *Hub) forget(e *entry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entries[e.key] == e {
		delete(h.entries, e.key)
	}
}

// Subscribers returns how many sessions currently follow the file at the
// validated resolved path through a shared reader, across compression formats.
func (h *Hub) Subscribers(resolvedPath string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for key, e := range h.entries {
		if key.path == resolvedPath {
			count += len(e.snapshot())
		}
	}
	return count
}

// entryFor returns the live entry for path, for tests.
func (h *Hub) entryFor(path string) *entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, e := range h.entries {
		if key.path == path {
			return e
		}
	}
	return nil
}
