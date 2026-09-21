package readhub

import (
	"context"
	"errors"
	"fmt"

	"github.com/mimecast/dtail/internal/ctxutil"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/omode"
)

// A subscriber whose queue is full when the shared reader publishes is
// evicted instead of making the reader, and so every other session of the
// file, wait for it. The evicted session handles what it has queued, then
// goes on with a private follow reader of its own that starts just past the
// last line it handled and feeds the session's filter and processor, so it
// loses no line and keeps its line numbering and local context.

// evict stops deliveries to sub, whose queue is full, and tells it to go on
// privately. The caller holds e.publishMu, so nothing is queued after the
// eviction. An entry left without subscribers stops.
func (e *entry) evict(sub *subscriber) {
	e.mu.Lock()
	remaining, found := e.detach(sub)
	if !found {
		e.mu.Unlock()
		return
	}
	close(sub.evicted)
	e.logger.Info(e.path, "Shared follow read evicted a slow subscriber", fmt.Sprintf("subscribers=%d", remaining))
	if e.owner == sub && remaining > 0 {
		e.handOver(e.subscribers[0])
	}
	e.mu.Unlock()

	if remaining == 0 {
		// The evicted session keeps the file's target valid until it
		// leaves, but nobody needs the shared reader any longer.
		e.onFailure(e)
		e.stop()
	}
}

// detach removes sub from the subscriber list and returns how many remain and
// whether sub was listed. The caller holds e.mu.
func (e *entry) detach(sub *subscriber) (remaining int, found bool) {
	for i, candidate := range e.subscribers {
		if candidate == sub {
			e.subscribers = append(e.subscribers[:i], e.subscribers[i+1:]...)
			found = true
			break
		}
	}
	return len(e.subscribers), found
}

// readPrivately goes on with a private follow reader of the session's own
// target, from where the session's read got to, until ctx ends. Like the read
// command's retry loop, it reads the file again after a read ended, e.g.
// after a rotation, with a new processor.
func (r *sessionRead) readPrivately(ctx context.Context) error {
	reader, err := r.privateReader()
	if err != nil {
		return err
	}
	for {
		err := reader.StartFiltered(ctx, r.filter)
		if err != nil {
			r.logger.Error(r.session.FilePath, r.session.GlobID, err)
			if errors.Is(err, fs.ErrReaderWorkerPanic) {
				return err
			}
		}
		if ctx.Err() != nil || !ctxutil.Sleep(ctx, r.options.RetryInterval) {
			return nil
		}
		r.logger.Info(r.session.FilePath, r.session.GlobID, "Reading file again")
		r.newProcessor()
	}
}

// privateReader makes the session's private follow reader, positioned just
// past the last line the session handled.
func (r *sessionRead) privateReader() (*fs.ReadFile, error) {
	target := r.session.Target
	options := fs.ReadOptions{
		Mode:           omode.TailClient,
		Target:         &target,
		FilePath:       r.session.FilePath,
		GlobID:         r.session.GlobID,
		ServerMessages: r.session.ServerMessages,
		MaxLineLength:  r.options.MaxLineLength,
		Logger:         r.messageLogger(),
	}
	if !r.at.known() {
		r.logger.Warn(r.session.FilePath, r.session.GlobID,
			"Position of the shared read unknown, following privately from the end of the file")
	}
	if current, err := endOfFile(target); err == nil && r.at.replacedBy(current) {
		r.logger.Warn(r.session.FilePath, r.session.GlobID,
			"File was rotated after the last line of the shared read, reading the new file from its beginning;"+
				" lines the old file had after that line, if any, are not read")
		// A private reader reads a new file with a new processor.
		r.newProcessor()
		r.at = position{offset: 0}
	}
	r.at.startAt(&options)
	reader, err := fs.NewReadFile(options)
	if err != nil {
		return nil, fmt.Errorf("private follow reader after the shared read: %w", err)
	}
	return reader, nil
}
