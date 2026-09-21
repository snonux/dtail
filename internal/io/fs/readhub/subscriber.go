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
	// missed is the item that did not fit into the queue, and held the file
	// at the path, opened at the eviction (nil if that failed); both are set
	// before evicted is closed.
	missed item
	held   *os.File
	// joinedAt is the end of the file when the session joined, where its
	// read starts, and skip tells it which published lines predate its join.
	// Both are set before the session's goroutine runs.
	joinedAt position
	skip     joinSkip
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
	reading := newSessionRead(s.session, logger, options, s.joinedAt, s.skip)
	defer reading.close()

	for {
		select {
		case <-ctx.Done():
			select {
			case <-s.evicted:
				closeHeld(s.held)
			default:
			}
			return nil
		case it := <-s.queue:
			goPrivate, err := reading.handle(ctx, it)
			if goPrivate {
				return reading.readPrivately(ctx, nil)
			}
			if err != nil {
				return err
			}
		case <-s.evicted:
			goPrivate, err := s.drain(ctx, reading)
			if err != nil {
				closeHeld(s.held)
				return err
			}
			if !goPrivate {
				reading.logger.Info(s.session.FilePath, s.session.GlobID,
					"Following privately after eviction from the shared read", "offset", reading.at.offset)
			}
			return reading.readPrivately(ctx, s.held)
		}
	}
}

// drain handles what was queued before the eviction, nothing is queued
// after it, and then the item that did not fit when it tells the session
// something its private reader would not see: the file was truncated or
// rotated, or the shared reader failed. A line the private reader reads
// again, and the long line warning it sends again, are left to it.
func (s *subscriber) drain(ctx context.Context, reading *sessionRead) (goPrivate bool, err error) {
	for {
		select {
		case it := <-s.queue:
			if goPrivate, err := reading.handle(ctx, it); goPrivate || err != nil {
				return goPrivate, err
			}
		default:
			switch s.missed.kind {
			case restartItem, reopenItem, failedItem:
				return reading.handle(ctx, s.missed)
			}
			return false, nil
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
	// handled, where a private reader would go on, or where it joined.
	at position
	// atLineEnd reports that at is the end of a line the session handled
	// rather than where it joined or the file started over.
	atLineEnd bool
	// skip: lines published before the session joined, which a private
	// follow read opened at the join would not have read.
	skip joinSkip
	// warningPending: a long line warning arrived while the session was
	// skipping; it is sent only if the split line it precedes is not skipped.
	warningPending bool
}

func newSessionRead(session Session, logger logging.Logger, options Options,
	joinedAt position, skip joinSkip) *sessionRead {

	processor := session.NewProcessor()
	return &sessionRead{
		session:   session,
		logger:    logger,
		options:   options,
		processor: processor,
		filter:    fs.NewLineFilter(session.LContext, processor, session.Regex, session.GlobID),
		at:        joinedAt,
		skip:      skip,
	}
}

// handle applies it to the session. goPrivate reports that the shared reader
// failed for good but the session can go on with a private reader.
func (r *sessionRead) handle(ctx context.Context, it item) (goPrivate bool, err error) {
	switch it.kind {
	case chunkItem:
		return false, r.feed(ctx, it.chunk)
	case restartItem:
		r.warningPending = false
		if r.skip.restart() {
			return false, nil
		}
		r.filter.Restart()
		r.at = position{offset: 0, file: r.at.file}
		r.atLineEnd = false
	case reopenItem:
		r.warningPending = false
		if r.skip.reopen() {
			return false, nil
		}
		// A private read ends, flushes and closes its processor, and the read
		// command starts the next read with a new one.
		r.newProcessor()
		r.at = position{offset: 0}
		r.atLineEnd = false
	case longLineItem:
		if r.skip.active {
			r.warningPending = true
			return false, nil
		}
		r.sendLongLineWarning(ctx)
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
func (r *sessionRead) feed(ctx context.Context, c *chunk) error {
	for i := range c.ends {
		end := c.lineEnd(i)
		if r.skip.skips(end) {
			// A warning pending belonged to this line.
			r.warningPending = false
			continue
		}
		if r.warningPending {
			r.warningPending = false
			r.sendLongLineWarning(ctx)
		}
		stop, err := r.filter.ProcessLine(c.line(i))
		r.at = end
		r.atLineEnd = true
		if err != nil {
			return err
		}
		if stop {
			return ErrStopped
		}
	}
	return r.filter.Flush()
}

func (r *sessionRead) sendLongLineWarning(ctx context.Context) {
	r.sendMessage(ctx, fs.LongLineWarning(r.messageLogger(), r.session.FilePath))
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
