package readhub

import (
	"context"
	"errors"
	"os"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/logging"
)

// subscriber is one session's subscription to a shared read. Its queue is
// drained by the session's own goroutine, so the sessions of one file filter
// and process lines in parallel.
type subscriber struct {
	session Session
	queue   chan item
	// done is closed when the subscriber left; publishers stop delivering.
	done chan struct{}
	// evicted is closed when the publisher found the queue full and stopped
	// delivering to the subscriber, which then goes on with a private read.
	evicted chan struct{}
	// joinedAt is the end of the file when the session joined, where its
	// read starts; skipToJoin tells it to skip published lines ending at or
	// before it. Both are set before the session's goroutine runs.
	joinedAt   position
	skipToJoin bool
}

func newSubscriber(session Session, queueChunks int) *subscriber {
	return &subscriber{
		session:  session,
		queue:    make(chan item, queueChunks),
		done:     make(chan struct{}),
		evicted:  make(chan struct{}),
		joinedAt: unknownPosition(),
	}
}

// offer queues it without waiting and reports whether it was queued, or need
// not be because the subscriber left.
func (s *subscriber) offer(it item) bool {
	select {
	case s.queue <- it:
		return true
	case <-s.done:
		return true
	default:
		return false
	}
}

// run feeds the session until ctx ends or its read ends like a private read
// would: with a max-count stop or an error. When the subscriber is evicted,
// or the shared reader fails, the session goes on with a private reader.
func (s *subscriber) run(ctx context.Context, logger logging.Logger, options Options) error {
	reading := newSessionRead(s.session, logger, options, s.joinedAt, s.skipToJoin)
	defer reading.close()

	for {
		select {
		case <-ctx.Done():
			return nil
		case it := <-s.queue:
			goPrivate, err := reading.handle(ctx, it)
			if goPrivate {
				return reading.readPrivately(ctx)
			}
			if err != nil {
				return err
			}
		case <-s.evicted:
			if err := s.drain(ctx, reading); err != nil {
				return err
			}
			reading.logger.Info(s.session.FilePath, s.session.GlobID,
				"Following privately after eviction from the shared read", "offset", reading.at.offset)
			return reading.readPrivately(ctx)
		}
	}
}

// drain handles what was queued before the eviction; nothing is queued after.
func (s *subscriber) drain(ctx context.Context, reading *sessionRead) error {
	for {
		select {
		case it := <-s.queue:
			goPrivate, err := reading.handle(ctx, it)
			if goPrivate {
				return nil
			}
			if err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// sessionRead applies one session's filter and processor to the shared
// lines, mirroring what the session's private reader and read command would
// do with them, and continues with a private reader when needed.
type sessionRead struct {
	session   Session
	logger    logging.Logger
	options   Options
	processor line.Processor
	filter    *fs.LineFilter
	// at is where the session's read got to: just past the last line it
	// handled, where a private reader would go on.
	at position
	// joinedAt and skipping: lines of the file the session joined at that end
	// at or before joinedAt were in the file before the session joined, and
	// a private follow read would not have read them.
	joinedAt position
	skipping bool
}

func newSessionRead(session Session, logger logging.Logger, options Options,
	joinedAt position, skipToJoin bool) *sessionRead {

	processor := session.NewProcessor()
	return &sessionRead{
		session:   session,
		logger:    logger,
		options:   options,
		processor: processor,
		filter:    fs.NewLineFilter(session.LContext, processor, session.Regex, session.GlobID),
		at:        joinedAt,
		joinedAt:  joinedAt,
		skipping:  skipToJoin && joinedAt.known() && joinedAt.offset > 0,
	}
}

// handle applies it to the session. goPrivate reports that the shared reader
// failed for good but the session can go on with a private reader.
func (r *sessionRead) handle(ctx context.Context, it item) (goPrivate bool, err error) {
	switch it.kind {
	case chunkItem:
		return false, r.feed(it.chunk)
	case restartItem:
		r.filter.Restart()
		r.skipping = false
		r.at = position{offset: 0, file: r.at.file}
	case reopenItem:
		// A private read ends, flushes and closes its processor, and the read
		// command starts the next read with a new one.
		r.newProcessor()
		r.skipping = false
		r.at = position{offset: 0}
	case longLineItem:
		r.sendMessage(ctx, fs.LongLineWarning(r.messageLogger(), r.session.FilePath))
	case failedItem:
		if errors.Is(it.err, fs.ErrReaderWorkerPanic) {
			return false, it.err
		}
		r.logger.Warn(r.session.FilePath, r.session.GlobID,
			"Shared follow read failed, going on with a private read", "offset", r.at.offset)
		return true, nil
	}
	return false, nil
}

// feed filters every line of c the session has not seen yet, then flushes,
// as a private follow reader does after every read.
func (r *sessionRead) feed(c *chunk) error {
	for i := range c.ends {
		end := c.lineEnd(i)
		if r.predatesJoin(end) {
			continue
		}
		stop, err := r.filter.ProcessLine(c.line(i))
		r.at = end
		if err != nil {
			return err
		}
		if stop {
			return ErrStopped
		}
	}
	return r.filter.Flush()
}

// predatesJoin reports whether a published line that ends at end was in the
// file before the session joined, so that a private follow read opened at the
// join would not have read it.
func (r *sessionRead) predatesJoin(end position) bool {
	if !r.skipping {
		return false
	}
	switch {
	case !end.known():
		// Without a position there is nothing to compare; deliver.
	case !os.SameFile(end.file, r.joinedAt.file):
		// The reader has not noticed yet that the path was rotated before
		// the join: the session starts at the end of the new file.
		return true
	case end.offset <= r.joinedAt.offset:
		return true
	}
	r.skipping = false
	return false
}

func (r *sessionRead) sendMessage(ctx context.Context, message string) {
	if r.session.ServerMessages == nil {
		return
	}
	select {
	case r.session.ServerMessages <- message:
	case <-ctx.Done():
	}
}

// messageLogger returns the logger that words the session's messages.
func (r *sessionRead) messageLogger() logging.Logger {
	if r.session.Logger != nil {
		return r.session.Logger
	}
	return r.logger
}

// newProcessor ends the current processor and feeds the filter's next read
// to a new one, as the read command does for every read of its retry loop.
func (r *sessionRead) newProcessor() {
	r.endProcessor()
	r.processor = r.session.NewProcessor()
	r.filter.Reopen(r.processor)
}

// close releases the filter and ends the current processor.
func (r *sessionRead) close() {
	r.filter.Close()
	r.endProcessor()
}

// endProcessor flushes and closes the processor, logging failures as the read
// command does for a private read.
func (r *sessionRead) endProcessor() {
	if err := r.processor.Flush(); err != nil {
		r.logger.Error(r.session.FilePath, r.session.GlobID, "flush error", err)
	}
	if err := r.processor.Close(); err != nil {
		r.logger.Error(r.session.FilePath, r.session.GlobID, "close error", err)
	}
}
