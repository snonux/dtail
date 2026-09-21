package handlers

import (
	"context"
	"errors"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/fs/readhub"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// groupReadFunc reads a file once for the members of a group; see
// readhub.Hub.ReadOnce.
type groupReadFunc func(context.Context, omode.Mode, readhub.Session, readhub.Group, readhub.SlotAcquirer) error

type readShareKeyType struct{}

var readShareKey readShareKeyType

// groupReadFuncFor returns hub's ReadOnce, or nil for no hub.
func groupReadFuncFor(hub *readhub.Hub) groupReadFunc {
	if hub == nil {
		return nil
	}
	return hub.ReadOnce
}

// withReadShareOption keeps the value of a command's config.ReadShareOption
// for the read it starts. Only scheduled jobs send the option.
func withReadShareOption(ctx context.Context, value string) context.Context {
	if value == "" {
		return ctx
	}
	return context.WithValue(ctx, readShareKey, value)
}

// readShareGroup returns the group whose one-shot read of a validated file
// the read may join. The group ID only selects a group read; the session
// joins it with its own validated target after its own permission check, and
// the group read takes a cat slot for the session, like a private read.
func (r *readCommand) readShareGroup(ctx context.Context, target *fs.ValidatedReadTarget) (readhub.Group, bool) {
	value, _ := ctx.Value(readShareKey).(string)
	if value == "" || r.readGroup == nil || r.serverless ||
		(r.mode != omode.CatClient && r.mode != omode.GrepClient) ||
		target == nil || target.Kind != fs.FileKind {
		return readhub.Group{}, false
	}
	share, err := config.ParseReadShare(value)
	if err != nil {
		r.logger.Warn(r.logContext, "Ignoring invalid read share option, reading privately", err)
		return readhub.Group{}, false
	}
	return readhub.Group{ID: share.Group, Members: share.Members}, true
}

// readWithGroup reads the file through its group's one-shot read. The caller
// holds no cat slot: the group read takes the session's slot with limiter once
// the group's members are there, so that no member holds a slot while it
// waits for the others. It reports false, having read nothing and holding no
// slot, when the group read had already started; the caller then takes a
// slot and reads privately.
func (r *readCommand) readWithGroup(ctx context.Context, ltx lcontext.LContext, re regex.Regex,
	options readerFactoryOptions, group readhub.Group, limiter readLimiter) bool {

	path, globID := options.path, options.globID
	r.logger.Info(r.logContext, "Using shared one-shot read", path, globID)
	err := r.readGroup(ctx, r.mode, readhub.Session{
		Target:         *options.target,
		FilePath:       path,
		GlobID:         globID,
		LContext:       ltx,
		Regex:          re,
		ServerMessages: options.serverMessages,
		Logger:         options.logger,
		NewProcessor: func() line.Processor {
			// Called once, and only after the session joined the group.
			return r.makeProcessor(path, globID, r.newLineWriter(ctx, r.generation))
		},
	}, group, func(slotCtx context.Context) (func(), bool) {
		return limiter(slotCtx, path)
	})
	switch {
	case errors.Is(err, readhub.ErrGroupReadStarted):
		return false
	case err == nil:
		return true
	}
	// A private one-shot read logs its reader's or processor's error and
	// ends, and a worker panic ends the command as a panic.
	r.logger.Error(r.logContext, path, globID, err)
	if errors.Is(err, fs.ErrReaderWorkerPanic) {
		panic(err)
	}
	return true
}
