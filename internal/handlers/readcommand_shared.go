package handlers

import (
	"context"
	"errors"

	"github.com/mimecast/dtail/internal/ctxutil"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/fs/readhub"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// followSharedFunc returns hub's Follow, or nil for no hub.
func followSharedFunc(hub *readhub.Hub) func(context.Context, readhub.Session) error {
	if hub == nil {
		return nil
	}
	return hub.Follow
}

// shouldShareRead reports whether the read may use the shared follow reader
// of its file instead of a private one. Only follow reads of a validated file
// in dserver share. A max-count limit keeps the read private: when the limit
// is reached, a private follow read starts over from the beginning of the
// file, which a shared read cannot do for one session.
func (r *readCommand) shouldShareRead(ltx lcontext.LContext, target *fs.ValidatedReadTarget) bool {
	return r.followShared != nil &&
		!r.serverless &&
		r.mode == omode.TailClient &&
		target != nil &&
		target.Kind == fs.FileKind &&
		ltx.MaxCount == 0
}

// readShared follows the file through its shared reader. When the shared
// read ends without the session's context ending, the read goes on as the
// private read loop would at that point.
func (r *readCommand) readShared(ctx context.Context, ltx lcontext.LContext, re regex.Regex,
	options readerFactoryOptions, privateReader fs.FileReader) {

	path, globID := options.path, options.globID
	r.logger.Info(r.logContext, "Using shared follow read", path, globID)
	writer := r.newLineWriter(ctx, r.generation)

	err := r.followShared(ctx, readhub.Session{
		Target:         *options.target,
		FilePath:       path,
		GlobID:         globID,
		LContext:       ltx,
		Regex:          re,
		ServerMessages: options.serverMessages,
		Logger:         options.logger,
		NewProcessor: func() line.Processor {
			return r.makeProcessor(path, globID, writer)
		},
	})
	if err == nil || ctx.Err() != nil {
		return
	}

	r.logger.Error(r.logContext, path, globID, err)
	switch {
	case errors.Is(err, fs.ErrReaderWorkerPanic):
		// The private read loop panics on a reader worker panic, too.
		panic(err)
	case errors.Is(err, readhub.ErrReaderFailed):
		// Nothing was wrong with the session; go on with a private follow
		// read. It starts at the end of the file as of now, so lines the
		// shared reader had not delivered yet are skipped.
		r.logger.Warn(r.logContext, "Shared follow read failed, reading privately from the end of the file",
			path, globID)
		r.executeReadLoop(ctx, ltx, path, globID, re, privateReader, r.readViaProcessor(path, globID, writer))
	default:
		r.restartPrivately(ctx, ltx, re, options, writer)
	}
}

// restartPrivately continues after the session's processor failed. The
// private read loop logs the error, waits, and starts the reader again, which
// reads the already opened file from its beginning; do the same with a
// private reader that starts at the beginning.
func (r *readCommand) restartPrivately(ctx context.Context, ltx lcontext.LContext, re regex.Regex,
	options readerFactoryOptions, writer LineWriter) {

	if !ctxutil.Sleep(ctx, r.timings.readRetryInterval) {
		return
	}
	r.logger.Info(options.path, options.globID, "Reading file again")
	reader, err := makeReader(options, omode.TailClient, false)
	if err != nil {
		r.sendServerMessage(ctx, r.logger.Warn(r.logContext, "Unable to create file reader", err))
		return
	}
	r.executeReadLoop(ctx, ltx, options.path, options.globID, re, reader,
		r.readViaProcessor(options.path, options.globID, writer))
}
