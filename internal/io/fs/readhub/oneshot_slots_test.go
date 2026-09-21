package readhub

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// slots is a cat slot limiter like dserver's, counting the slots in use.
type slots struct {
	limiter chan struct{}
	mu      sync.Mutex
	maxUsed int
}

func newSlots(capacity int) *slots {
	return &slots{limiter: make(chan struct{}, capacity)}
}

func (s *slots) acquire(ctx context.Context) (func(), bool) {
	select {
	case s.limiter <- struct{}{}:
	case <-ctx.Done():
		return nil, false
	}
	s.mu.Lock()
	s.maxUsed = max(s.maxUsed, len(s.limiter))
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { <-s.limiter }) }, true
}

func (s *slots) inUse() int { return len(s.limiter) }

func TestReadOnceMembersHoldNoSlotWhileTheyWait(t *testing.T) {
	path := writeFile(t, "a\nb\n")
	hub := newGroupHub(time.Hour)
	seams := &countingSeams{release: make(chan struct{})}
	seams.install(hub)
	catSlots := newSlots(2)
	group := Group{ID: "g", Members: 2}

	first := startSlottedMember(t, hub, omode.CatClient, path, lcontext.LContext{}, regex.NewNoop(),
		group, catSlots.acquire)
	waitFor(t, "the first member to join", func() bool { return groupMembers(hub, path) == 1 })
	time.Sleep(20 * time.Millisecond)
	if used := catSlots.inUse(); used != 0 {
		t.Fatalf("a waiting member holds %d cat slots, want none", used)
	}

	second := startSlottedMember(t, hub, omode.CatClient, path, lcontext.LContext{}, regex.NewNoop(),
		group, catSlots.acquire)
	waitFor(t, "the group read to start", func() bool { return seams.starts() == 1 })
	if used := catSlots.inUse(); used != 2 {
		t.Errorf("the group read holds %d cat slots, want one per member", used)
	}
	close(seams.release)
	for _, m := range []*testMember{first, second} {
		if err := m.wait(t); err != nil {
			t.Fatalf("ReadOnce() = %v", err)
		}
		if got := m.recorder.lines(); !reflect.DeepEqual(got, []string{"a\n", "b\n"}) {
			t.Errorf("lines = %q", got)
		}
	}
	if used := catSlots.inUse(); used != 0 {
		t.Errorf("%d cat slots still in use after the read, want none", used)
	}
}

// Two sessions each read the same files, joining the group reads in opposite
// orders, with fewer cat slots than files times sessions. A member that held
// a slot while it waited for the other session would keep that session's
// member from joining; the group reads would only start after the group wait
// (an hour here) with one member each.
func TestReadOnceGroupReadsNeverWaitForEachOthersSlots(t *testing.T) {
	const files = 6
	hub := newGroupHub(time.Hour)
	catSlots := newSlots(2)
	var paths []string
	for range files {
		paths = append(paths, writeFile(t, "a\nb\nc\n"))
	}
	group := Group{ID: "g", Members: 2}
	var members []*testMember
	for i := range files {
		members = append(members,
			startSlottedMember(t, hub, omode.CatClient, paths[i], lcontext.LContext{}, regex.NewNoop(),
				group, catSlots.acquire),
			startSlottedMember(t, hub, omode.CatClient, paths[files-1-i], lcontext.LContext{}, regex.NewNoop(),
				group, catSlots.acquire))
	}
	for _, m := range members {
		if err := m.wait(t); err != nil {
			t.Fatalf("ReadOnce() = %v", err)
		}
		if got := m.recorder.lines(); !reflect.DeepEqual(got, []string{"a\n", "b\n", "c\n"}) {
			t.Errorf("lines = %q", got)
		}
	}
	if catSlots.maxUsed > 2 {
		t.Errorf("up to %d cat slots were in use, want at most 2", catSlots.maxUsed)
	}
	if used := catSlots.inUse(); used != 0 {
		t.Errorf("%d cat slots still in use, want none", used)
	}
}

func TestReadOnceAdmitsAtMostMaxGroupMembers(t *testing.T) {
	path := writeFile(t, "a\n")
	logger := &lineLogger{}
	hub := New(Options{Logger: logger, GroupWait: time.Hour, MaxGroupMembers: 2})
	seams := &countingSeams{release: make(chan struct{})}
	seams.install(hub)
	group := Group{ID: "secret-group-id", Members: 3}

	// Two of three members fill the group: its read starts without waiting
	// an hour for the third.
	members := []*testMember{
		startGroupMember(t, hub, omode.CatClient, path, lcontext.LContext{}, regex.NewNoop(), group),
		startGroupMember(t, hub, omode.CatClient, path, lcontext.LContext{}, regex.NewNoop(), group),
	}
	waitFor(t, "the group read to start", func() bool { return seams.starts() == 1 })
	third := groupSession(t, path, lcontext.LContext{}, regex.NewNoop(), (&recorder{}).newProcessor, nil)
	if err := hub.ReadOnce(context.Background(), omode.CatClient, third, group, freeSlot); !errors.Is(err, ErrGroupReadStarted) {
		t.Errorf("third ReadOnce() = %v, want ErrGroupReadStarted", err)
	}
	close(seams.release)
	for _, m := range members {
		if err := m.wait(t); err != nil {
			t.Fatalf("ReadOnce() = %v", err)
		}
	}
	if logger.count("Shared one-shot read started", group.LogID(), "members=2/2") != 1 {
		t.Errorf("log = %q, want one read started with members=2/2", logger.all())
	}
	if strings.Contains(strings.Join(logger.all(), "\n"), group.ID) {
		t.Errorf("the log contains the group ID: %q", logger.all())
	}
}

func TestGroupEntryRefusesMembersBeyondMaxGroupMembers(t *testing.T) {
	path := writeFile(t, "a\n")
	session := groupSession(t, path, lcontext.LContext{}, regex.NewNoop(), (&recorder{}).newProcessor, nil)
	var slotsMu sync.Mutex
	e := newGroupEntry(groupKey{path: path}, Group{ID: "g", Members: 3}, path,
		Options{MaxGroupMembers: 2}, logging.NopLogger{}, defaultSeams(), &slotsMu)
	for i, want := range []bool{true, true, false} {
		member := &groupMember{subscriber: newSubscriber(session, 1), acquire: freeSlot}
		if got := e.add(member); got != want {
			t.Errorf("member %d joined = %v, want %v", i+1, got, want)
		}
	}
}

func TestReadOnceRemembersGroupReadsThatEnded(t *testing.T) {
	path := writeFile(t, "a\n")
	const memory = 300 * time.Millisecond
	hub := New(Options{GroupWait: 10 * time.Millisecond, GroupMemory: memory})
	group := Group{ID: "g", Members: 2}

	// The only member reads alone after the group wait.
	alone := startGroupMember(t, hub, omode.CatClient, path, lcontext.LContext{}, regex.NewNoop(), group)
	if err := alone.wait(t); err != nil {
		t.Fatalf("ReadOnce() = %v", err)
	}
	ended := time.Now()

	// The other member arrives after the read ended: it reads privately at
	// once instead of waiting for a group read of its own.
	late := groupSession(t, path, lcontext.LContext{}, regex.NewNoop(), (&recorder{}).newProcessor, nil)
	if err := hub.ReadOnce(context.Background(), omode.CatClient, late, group, freeSlot); !errors.Is(err, ErrGroupReadStarted) {
		t.Fatalf("late ReadOnce() = %v, want ErrGroupReadStarted", err)
	}
	if time.Since(ended) >= memory {
		t.Skip("the late member came too late for this test to tell anything")
	}

	// Once the hub forgot the read, the group ID starts a new group read.
	time.Sleep(memory)
	again := startGroupMember(t, hub, omode.CatClient, path, lcontext.LContext{}, regex.NewNoop(), group)
	if err := again.wait(t); err != nil {
		t.Fatalf("ReadOnce() after the group memory = %v", err)
	}
	if got := again.recorder.lines(); !reflect.DeepEqual(got, []string{"a\n"}) {
		t.Errorf("lines = %q", got)
	}
	hub.mu.Lock()
	remembered := len(hub.oneshot.ended)
	hub.mu.Unlock()
	if remembered != 1 {
		t.Errorf("the hub remembers %d ended group reads, want only the last one", remembered)
	}
}

func TestReadOnceMemberLeavingWhileWaitingForItsSlot(t *testing.T) {
	path := writeFile(t, "a\n")
	hub := newGroupHub(time.Hour)
	catSlots := newSlots(1)
	group := Group{ID: "g", Members: 2}

	// Another read holds the only slot.
	release, _ := catSlots.acquire(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	leaving := &recorder{}
	leavingDone := make(chan error, 1)
	session := groupSession(t, path, lcontext.LContext{}, regex.NewNoop(), leaving.newProcessor, nil)
	go func() { leavingDone <- hub.ReadOnce(ctx, omode.CatClient, session, group, catSlots.acquire) }()
	staying := startSlottedMember(t, hub, omode.CatClient, path, lcontext.LContext{}, regex.NewNoop(),
		group, catSlots.acquire)
	waitFor(t, "both members to join", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		for _, e := range hub.oneshot.entries {
			e.mu.Lock()
			started := e.started
			e.mu.Unlock()
			return started
		}
		return false
	})

	cancel()
	if err := <-leavingDone; err != nil {
		t.Errorf("leaving ReadOnce() = %v, want nil", err)
	}
	release()
	if err := staying.wait(t); err != nil {
		t.Fatalf("ReadOnce() = %v", err)
	}
	if got := staying.recorder.lines(); !reflect.DeepEqual(got, []string{"a\n"}) {
		t.Errorf("lines = %q", got)
	}
	if len(leaving.lines()) != 0 {
		t.Error("the member that left got lines")
	}
	if used := catSlots.inUse(); used != 0 {
		t.Errorf("%d cat slots still in use, want none", used)
	}
}

// lineLogger records every log line.
type lineLogger struct {
	textLogger
	mu    sync.Mutex
	lines []string
}

func (l *lineLogger) Info(args ...any) string {
	message := fmt.Sprint(args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, message)
	return message
}

func (l *lineLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *lineLogger) count(substrings ...string) int {
	count := 0
	for _, line := range l.all() {
		matches := true
		for _, s := range substrings {
			matches = matches && strings.Contains(line, s)
		}
		if matches {
			count++
		}
	}
	return count
}
