package readhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// DefaultGroupMemory is how long the hub remembers, by default, a group read
// that ended, so that a member arriving after it ended reads privately at
// once instead of waiting GroupWait for a new group read of its own.
const DefaultGroupMemory = 10 * time.Minute

// ErrGroupReadStarted reports that the group's shared read had already
// started, had all the members it admits, or ended when the session came to
// join it. The session did not join and reads the file on its own, which
// gives the same result.
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

// LogID returns a short hash of the group ID for log lines: the ID itself
// stays out of the logs, which still tell the reads of one group apart.
func (g Group) LogID() string {
	sum := sha256.Sum256([]byte(g.ID))
	return "group=" + hex.EncodeToString(sum[:4])
}

// SlotAcquirer waits for one of dserver's cat slots for a group member and
// returns the function that releases it. It reports false, holding no slot,
// once ctx ended.
type SlotAcquirer func(ctx context.Context) (release func(), acquired bool)

// groupReads is the hub's state of the one-shot group reads.
type groupReads struct {
	// entries holds the group reads that have not ended yet.
	entries map[groupKey]*groupEntry
	// ended remembers when group reads ended, until DefaultGroupMemory
	// (Options.GroupMemory) passed.
	ended map[groupKey]time.Time
	// slotsMu lets one group read at a time take its members' cat slots, so
	// that two group reads never wait for each other's slots.
	slotsMu sync.Mutex
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

	// expected is how many members the read waits for: the group's members,
	// at most Options.MaxGroupMembers.
	expected int
	// slotsMu is the hub's groupReads.slotsMu.
	slotsMu *sync.Mutex

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
	members []*groupMember
	// started is set when the read begins; nobody joins afterwards.
	started bool

	// publishMu keeps every member's queue in publication order.
	publishMu sync.Mutex
}

var _ publisher = (*groupEntry)(nil)

// groupMember is a session in a group read.
type groupMember struct {
	*subscriber
	// acquire takes the member's cat slot, release gives it back. release is
	// set, under the entry's mutex, once the slot was taken.
	acquire SlotAcquirer
	release func()
}

func newGroupEntry(key groupKey, group Group, path string, options Options,
	logger logging.Logger, seams hubSeams, slotsMu *sync.Mutex) *groupEntry {

	expected := group.Members
	if options.MaxGroupMembers > 0 {
		expected = min(expected, options.MaxGroupMembers)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &groupEntry{
		key:      key,
		group:    group,
		path:     path,
		options:  options,
		logger:   logger,
		seams:    seams,
		expected: expected,
		slotsMu:  slotsMu,
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
// at most Options.MaxGroupMembers, or DefaultGroupWait (Options.GroupWait)
// after its first member joined.
//
// A member holds no cat slot while it waits for the others: the caller must
// not hold one. Once the members are there, the group read takes one cat slot
// for every member with the member's acquire, as a private read of each
// member would, and only then reads; the member's slot is released when the
// member's read ended. Group reads take their slots one group at a time, and
// a member holds nothing else while its group waits for slots, so group reads
// never wait for each other's slots; with Options.MaxGroupMembers at most the
// server's cat slots, a group read always gets its slots once the reads
// holding them ended.
//
// ReadOnce blocks like a private one-shot read. It returns nil when the read
// ended, when the session's max-count limit ended it, or when ctx ended; the
// reader's error otherwise, which wraps fs.ErrReaderWorkerPanic if the shared
// reader panicked, or a processor error of the session. It returns
// ErrGroupReadStarted, without having fed the session anything, when the
// group read had already started, had all the members it admits, or ended
// less than DefaultGroupMemory (Options.GroupMemory) ago; the caller then
// takes a slot and reads privately.
//
// mode is omode.CatClient or omode.GrepClient; both read the same way. Each
// member applies its own regex, local context and line numbering, from line 1,
// to the lines in the form the private snapshot reader feeds them: with their
// newline, empty lines included.
func (h *Hub) ReadOnce(ctx context.Context, mode omode.Mode, session Session, group Group,
	acquire SlotAcquirer) error {

	if err := validateGroupRead(mode, session, group, acquire); err != nil {
		return err
	}
	member := &groupMember{subscriber: newSubscriber(session, h.options.QueueChunks), acquire: acquire}
	e, joined := h.joinGroup(mode, group, member)
	if !joined {
		return ErrGroupReadStarted
	}
	defer h.leaveGroup(e, member)
	return member.readOnce(ctx, h.logger)
}

func validateGroupRead(mode omode.Mode, session Session, group Group, acquire SlotAcquirer) error {
	switch {
	case acquire == nil:
		return errors.New("shared one-shot read requires a slot acquirer")
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
// reports false when the group read had already started, was full, or ended
// recently.
func (h *Hub) joinGroup(mode omode.Mode, group Group, member *groupMember) (*groupEntry, bool) {
	key := groupKey{
		path:        member.session.Target.ResolvedPath(),
		compression: fs.CompressionFormat(member.session.FilePath),
		mode:        mode,
		group:       group.ID,
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	reads := &h.oneshot
	if reads.entries == nil {
		reads.entries = make(map[groupKey]*groupEntry)
		reads.ended = make(map[groupKey]time.Time)
	}
	if h.endedRecently(key) {
		h.logger.Info(member.session.FilePath, "Shared one-shot read already ended, reading privately",
			group.LogID())
		return nil, false
	}
	e := reads.entries[key]
	if e == nil {
		e = newGroupEntry(key, group, member.session.FilePath, h.options, h.logger, h.seams, &reads.slotsMu)
		reads.entries[key] = e
		e.add(member)
		go e.run()
		return e, true
	}
	if !e.add(member) {
		h.logger.Info(member.session.FilePath, "Shared one-shot read already started or full, reading privately",
			group.LogID())
		return nil, false
	}
	return e, true
}

// endedRecently reports whether the group read of key ended less than the
// group memory ago, and forgets the group reads that ended longer ago. The
// caller holds h.mu.
func (h *Hub) endedRecently(key groupKey) bool {
	memory := h.options.GroupMemory
	if memory <= 0 {
		memory = DefaultGroupMemory
	}
	now := time.Now()
	for candidate, ended := range h.oneshot.ended {
		if now.Sub(ended) >= memory {
			delete(h.oneshot.ended, candidate)
		}
	}
	_, ok := h.oneshot.ended[key]
	return ok
}

// leaveGroup removes member from e. When the last member left, the hub
// forgets e, remembering that its read ended if it started, and e's read
// stops.
func (h *Hub) leaveGroup(e *groupEntry, member *groupMember) {
	h.mu.Lock()
	defer h.mu.Unlock()
	remaining, started := e.remove(member)
	if remaining != 0 {
		return
	}
	if h.oneshot.entries[e.key] == e {
		delete(h.oneshot.entries, e.key)
		if started {
			h.oneshot.ended[e.key] = time.Now()
		}
	}
	e.cancel()
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

// add registers member unless the read started already or has all the
// members it waits for.
func (e *groupEntry) add(member *groupMember) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started || len(e.members) >= e.expected {
		return false
	}
	e.members = append(e.members, member)
	count := len(e.members)
	e.logger.Info(e.path, "Shared one-shot read gained a member", e.group.LogID(),
		fmt.Sprintf("members=%d/%d", count, e.expected))
	if count >= e.expected {
		e.completeOnce.Do(func() { close(e.complete) })
	}
	return true
}

// remove unregisters member, stops deliveries to it, releases its cat slot
// and returns how many members remain and whether the read started.
func (e *groupEntry) remove(member *groupMember) (remaining int, started bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, candidate := range e.members {
		if candidate == member {
			e.members = append(e.members[:i], e.members[i+1:]...)
			break
		}
	}
	close(member.done)
	if member.release != nil {
		member.release()
		member.release = nil
	}
	return len(e.members), e.started
}

// begin closes the group and returns its members.
func (e *groupEntry) begin() []*groupMember {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.started = true
	return append([]*groupMember(nil), e.members...)
}

func (e *groupEntry) snapshot() []*groupMember {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*groupMember(nil), e.members...)
}

// acquireSlots takes a cat slot for each of members, one group read at a
// time, and returns the members that got one; a member that left while it
// waited is not among them.
func (e *groupEntry) acquireSlots(members []*groupMember) []*groupMember {
	e.slotsMu.Lock()
	defer e.slotsMu.Unlock()
	holding := members[:0]
	for _, member := range members {
		if e.acquireSlot(member) {
			holding = append(holding, member)
		}
	}
	return holding
}

// acquireSlot waits for member's cat slot until the member or the whole
// group left.
func (e *groupEntry) acquireSlot(member *groupMember) bool {
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	go func() {
		select {
		case <-member.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	release, acquired := member.acquire(ctx)
	if !acquired {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-member.done:
		// The member left meanwhile and will not release the slot.
		release()
		return false
	default:
	}
	member.release = release
	return true
}

// run waits for the members, reads the file once and tells every member that
// the read ended.
func (e *groupEntry) run() {
	defer close(e.done)
	defer e.cancel()
	if !e.awaitMembers() {
		return
	}
	members := e.acquireSlots(e.begin())
	if len(members) == 0 {
		return
	}
	e.logger.Info(e.path, "Shared one-shot read started", e.group.LogID(),
		fmt.Sprintf("members=%d/%d", len(members), e.expected))
	// The members joined with their own validated targets for the same
	// resolved path; the reader opens the file with the first one's.
	err := e.read(members[0].session)
	e.publish(item{kind: failedItem, err: err})
	e.logger.Info(e.path, "Shared one-shot read ended", e.group.LogID())
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
