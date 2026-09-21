package readhub

import (
	"context"

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
}

func newSubscriber(session Session, queueChunks int) *subscriber {
	return &subscriber{
		session: session,
		queue:   make(chan item, queueChunks),
		done:    make(chan struct{}),
	}
}

// deliver queues it unless the subscriber left.
func (s *subscriber) deliver(it item) {
	select {
	case s.queue <- it:
	case <-s.done:
	}
}

// run feeds the session until ctx ends or its read ends like a private read
// would: with a max-count stop or an error.
func (s *subscriber) run(ctx context.Context, logger logging.Logger) error {
	reading := newSessionRead(s.session, logger)
	defer reading.close()

	for {
		select {
		case <-ctx.Done():
			return nil
		case it := <-s.queue:
			if err := reading.handle(ctx, it); err != nil {
				return err
			}
		}
	}
}

// sessionRead applies one session's filter and processor to the shared
// lines, mirroring what the session's private reader and read command would
// do with them.
type sessionRead struct {
	session   Session
	logger    logging.Logger
	processor line.Processor
	filter    *fs.LineFilter
}

func newSessionRead(session Session, logger logging.Logger) *sessionRead {
	processor := session.NewProcessor()
	return &sessionRead{
		session:   session,
		logger:    logger,
		processor: processor,
		filter:    fs.NewLineFilter(session.LContext, processor, session.Regex, session.GlobID),
	}
}

func (r *sessionRead) handle(ctx context.Context, it item) error {
	switch it.kind {
	case chunkItem:
		return r.feed(it.chunk)
	case restartItem:
		r.filter.Restart()
	case reopenItem:
		// A private read ends, flushes and closes its processor, and the read
		// command starts the next read with a new one.
		r.endProcessor()
		r.processor = r.session.NewProcessor()
		r.filter.Reopen(r.processor)
	case longLineItem:
		return r.sendMessage(ctx, fs.LongLineWarning(r.messageLogger(), r.session.FilePath))
	case failedItem:
		return it.err
	}
	return nil
}

// feed filters every line of c, then flushes, as a private follow reader does
// after every read.
func (r *sessionRead) feed(c *chunk) error {
	for i := range c.ends {
		stop, err := r.filter.ProcessLine(c.line(i))
		if err != nil {
			return err
		}
		if stop {
			return ErrStopped
		}
	}
	return r.filter.Flush()
}

func (r *sessionRead) sendMessage(ctx context.Context, message string) error {
	if r.session.ServerMessages == nil {
		return nil
	}
	select {
	case r.session.ServerMessages <- message:
	case <-ctx.Done():
	}
	return nil
}

// messageLogger returns the logger that words the session's messages.
func (r *sessionRead) messageLogger() logging.Logger {
	if r.session.Logger != nil {
		return r.session.Logger
	}
	return r.logger
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
