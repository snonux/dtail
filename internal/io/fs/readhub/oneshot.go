package readhub

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/omode"
)

// DefaultGroupWait is how long a one-shot group read waits, by default, for
// all its members to join before it reads with the members it has.
const DefaultGroupWait = 3 * time.Second

// ErrGroupReadStarted reports that the group's shared read had already
// started when the session came to join it. The session did not join and
// reads the file on its own, which gives the same result.
var ErrGroupReadStarted = errors.New("shared one-shot read of the group already started")

// Group names the sessions that read a file once together, such as the
// scheduled jobs one dserver starts at the same time on the same files. The
// group ID only selects which group read a session joins; it grants no
// access: every member joins with its own validated target.
type Group struct {
	// ID is the group's identifier, random per group.
	ID string
	// Members is how many sessions the group read waits for.
	Members int
}

// groupKey identifies one group read: the same file, read in the same mode,
// by the same group.
type groupKey struct {
	path        string
	compression string
	mode        omode.Mode
	group       string
}

// groupEntry is the one-shot read of a file shared by one group. It waits
// until the group's members joined, or GroupWait passed, then reads the file
// once from its beginning and fans the lines out to every member. Delivery
// blocks while a member's queue is full: the members are jobs of the same
// dserver, so a slow one slows the others down instead of being dropped.
type groupEntry struct {
	key     groupKey
	group   Group
	path    string
	options Options
	logger  logging.Logger
	seams   hubSeams

	messages chan string
	ctx      context.Context
	cancel   context.CancelFunc
	// complete is closed once every expected member joined.
	complete     chan struct{}
	completeOnce sync.Once
	// done is closed once the read ended and the file is closed.
	done chan struct{}

	// mu guards the member list and started; the hub's mutex is always taken
	// first when both are needed.
	mu      sync.Mutex
	members []*subscriber
	// started is set when the read begins; nobody joins afterwards.
	started bool

	// publishMu keeps every member's queue in publication order.
	publishMu sync.Mutex
}

var _ publisher = (*groupEntry)(nil)

func newGroupEntry(key groupKey, group Group, path string, options Options,
	logger logging.Logger, seams hubSeams) *groupEntry {

	ctx, cancel := context.WithCancel(context.Background())
	return &groupEntry{
		key:      key,
		group:    group,
		path:     path,
		options:  options,
		logger:   logger,
		seams:    seams,
		messages: make(chan string),
		ctx:      ctx,
		cancel:   cancel,
		complete: make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// ReadOnce feeds session with every line of its file, read once from the
// beginning like a private cat or grep read, by the one read the members of
// group share. The group read starts once group.Members sessions joined it,
// or DefaultGroupWait (Options.GroupWait) after its first member joined.
//
// ReadOnce blocks like a private one-shot read. It returns nil when the read
// ended, when the session's max-count limit ended it, or when ctx ended; the
// reader's error otherwise, which wraps fs.ErrReaderWorkerPanic if the shared
// reader panicked, or a processor error of the session. It returns
// ErrGroupReadStarted, without having fed the session anything, when the
// group read had already started; the caller then reads privately.
//
// mode is omode.CatClient or omode.GrepClient; both read the same way. Each
// member applies its own regex, local context and line numbering, from line 1,
// to the lines in the form the private snapshot reader feeds them: with their
// newline, empty lines included.
func (h *Hub) ReadOnce(ctx context.Context, mode omode.Mode, session Session, group Group) error {
	if err := validateGroupRead(mode, session, group); err != nil {
		return err
	}
	member := newSubscriber(session, h.options.QueueChunks)
	e, joined := h.joinGroup(mode, group, member)
	if !joined {
		return ErrGroupReadStarted
	}
	defer h.leaveGroup(e, member)
	return member.readOnce(ctx, h.logger)
}

func validateGroupRead(mode omode.Mode, session Session, group Group) error {
	switch {
	case mode != omode.CatClient && mode != omode.GrepClient:
		return fmt.Errorf("shared one-shot read requires cat or grep mode, got %s", mode)
	case group.ID == "":
		return errors.New("shared one-shot read requires a group ID")
	case group.Members < 1:
		return fmt.Errorf("shared one-shot read requires at least one member, got %d", group.Members)
	}
	return validateSession(session)
}

// joinGroup adds member to its group read, starting one if needed. It
// reports false when the group read had already started.
func (h *Hub) joinGroup(mode omode.Mode, group Group, member *subscriber) (*groupEntry, bool) {
	key := groupKey{
		path:        member.session.Target.ResolvedPath(),
		compression: fs.CompressionFormat(member.session.FilePath),
		mode:        mode,
		group:       group.ID,
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.groups == nil {
		h.groups = make(map[groupKey]*groupEntry)
	}
	e := h.groups[key]
	if e == nil {
		e = newGroupEntry(key, group, member.session.FilePath, h.options, h.logger, h.seams)
		h.groups[key] = e
		e.add(member)
		go e.run()
		return e, true
	}
	if !e.add(member) {
		h.logger.Info(member.session.FilePath, "Shared one-shot read already started, reading privately",
			"group="+group.ID)
		return nil, false
	}
	return e, true
}

// leaveGroup removes member from e. When the last member left, the hub
// forgets e and e's read stops.
func (h *Hub) leaveGroup(e *groupEntry, member *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if remaining := e.remove(member); remaining == 0 {
		if h.groups[e.key] == e {
			delete(h.groups, e.key)
		}
		e.cancel()
	}
}

// readOnce feeds the session the group read's items until the read ended,
// the session's own read ended, or ctx ended.
func (s *subscriber) readOnce(ctx context.Context, logger logging.Logger) error {
	reading := newSessionRead(s.session, logger)
	defer reading.close()

	for {
		select {
		case <-ctx.Done():
			return nil
		case it := <-s.queue:
			if it.kind == failedItem {
				// The end of a group read: err is nil when it read the file
				// to its end.
				return it.err
			}
			err := reading.handle(ctx, it)
			if errors.Is(err, ErrStopped) {
				// A private one-shot read simply ends at its max-count limit.
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
}

// add registers member unless the read started already.
func (e *groupEntry) add(member *subscriber) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return false
	}
	e.members = append(e.members, member)
	count := len(e.members)
	e.logger.Info(e.path, "Shared one-shot read gained a member", "group="+e.group.ID,
		fmt.Sprintf("members=%d/%d", count, e.group.Members))
	if count >= e.group.Members {
		e.completeOnce.Do(func() { close(e.complete) })
	}
	return true
}

// remove unregisters member, stops deliveries to it and returns how many
// members remain.
func (e *groupEntry) remove(member *subscriber) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, candidate := range e.members {
		if candidate == member {
			e.members = append(e.members[:i], e.members[i+1:]...)
			break
		}
	}
	close(member.done)
	return len(e.members)
}

// begin closes the group and returns its members.
func (e *groupEntry) begin() []*subscriber {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.started = true
	return append([]*subscriber(nil), e.members...)
}

func (e *groupEntry) snapshot() []*subscriber {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*subscriber(nil), e.members...)
}

// run waits for the members, reads the file once and tells every member that
// the read ended.
func (e *groupEntry) run() {
	defer close(e.done)
	defer e.cancel()
	if !e.awaitMembers() {
		return
	}
	members := e.begin()
	if len(members) == 0 {
		return
	}
	e.logger.Info(e.path, "Shared one-shot read started", "group="+e.group.ID,
		fmt.Sprintf("members=%d/%d", len(members), e.group.Members))
	// The members joined with their own validated targets for the same
	// resolved path; the reader opens the file with the first one's.
	err := e.read(members[0].session)
	e.publish(item{kind: failedItem, err: err})
	e.logger.Info(e.path, "Shared one-shot read ended", "group="+e.group.ID)
}

// awaitMembers waits until every member joined or the wait timed out. It
// reports false when every member left before.
func (e *groupEntry) awaitMembers() bool {
	wait := e.options.GroupWait
	if wait <= 0 {
		wait = DefaultGroupWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-e.complete:
		return true
	case <-timer.C:
		return true
	case <-e.ctx.Done():
		return false
	}
}

// read runs the one read of the file. A panic in the reader becomes an error
// wrapping fs.ErrReaderWorkerPanic, as it would from a private reader's
// worker, so that it ends the members' reads instead of crashing dserver.
func (e *groupEntry) read(owner Session) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: shared one-shot reader: %v", fs.ErrReaderWorkerPanic, recovered)
			e.logger.Error(e.path, err, "stack", string(debug.Stack()))
		}
	}()

	target := owner.Target
	reader, err := fs.NewReadFile(fs.ReadOptions{
		Mode:           e.key.mode,
		Target:         &target,
		FilePath:       owner.FilePath,
		GlobID:         owner.GlobID,
		ServerMessages: e.messages,
		MaxLineLength:  e.options.MaxLineLength,
		Logger:         e.logger,
	})
	if err != nil {
		return fmt.Errorf("shared one-shot reader: %w", err)
	}
	stopForwarding := e.forwardMessages()
	defer stopForwarding()

	fanout := newFanoutProcessor(e)
	err = e.seams.startReader(e.ctx, reader, fanout)
	fanout.publishPending()
	return err
}

// forwardMessages tells every member when the reader warns about a long line
// until the returned function is called, which waits until the forwarder
// published every warning it received.
func (e *groupEntry) forwardMessages() (stop func()) {
	stopped := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-e.messages:
				e.publish(item{kind: longLineItem})
			case <-stopped:
				return
			}
		}
	}()
	return func() {
		close(stopped)
		<-finished
	}
}

// publish delivers it to every current member, in publication order. It
// waits while a member's queue is full, until the member took the item or
// left.
func (e *groupEntry) publish(it item) {
	e.publishMu.Lock()
	defer e.publishMu.Unlock()
	for _, member := range e.snapshot() {
		select {
		case member.queue <- it:
		case <-member.done:
		}
	}
}
