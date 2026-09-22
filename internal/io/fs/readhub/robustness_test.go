package readhub

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/regex"
)

func TestJoinReleasesHubLockWhenReaderConstructionPanics(t *testing.T) {
	hub := newTestHub()
	file := newTestFile(t)
	session := Session{
		Target: file.target(), FilePath: file.path + ".gz",
		NewProcessor: (&recorder{}).newProcessor,
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("join did not panic on an invalid compressed start offset")
			}
		}()
		hub.join(newSubscriber(session, hub.options.QueueChunks))
	}()
	if !hub.mu.TryLock() {
		t.Fatal("join left the hub locked after reader construction panicked")
	}
	hub.mu.Unlock()

	// A later valid follow must still be able to create and leave an entry.
	follower := startFollower(t, hub, file, lcontext.LContext{}, regex.NewNoop(), "valid")
	waitFor(t, "valid follower to join", func() bool { return subscriberCount(hub, file.path) == 1 })
	if err := follower.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestNewDefaultsRetryIntervalForFailedFollowReads(t *testing.T) {
	hub := New(Options{})
	if hub.options.RetryInterval != defaultRetryInterval {
		t.Fatalf("retry interval = %s, want %s", hub.options.RetryInterval, defaultRetryInterval)
	}
	file := newTestFile(t)
	attempts := make(chan struct{}, 16)
	hub.seams.startReader = func(context.Context, *fs.ReadFile, line.Processor) error {
		select {
		case attempts <- struct{}{}:
		default:
		}
		return errors.New("injected read failure")
	}
	follower := startFollower(t, hub, file, lcontext.LContext{}, regex.NewNoop(), "retry")
	select {
	case <-attempts:
	case <-time.After(waitTimeout):
		t.Fatal("the first read did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(attempts); got != 0 {
		t.Errorf("%d immediate retries after a failed read, want none", got)
	}
	if err := follower.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestStartRejoinedOpensThroughHubSeam(t *testing.T) {
	hub := newTestHub()
	file := newTestFile(t)
	at, err := endOfFile(file.target())
	if err != nil {
		t.Fatal(err)
	}
	session := Session{Target: file.target(), FilePath: file.path, NewProcessor: (&recorder{}).newProcessor}
	sub := newSubscriber(session, hub.options.QueueChunks)
	opened := false
	hub.seams.openFile = func(fs.ValidatedReadTarget) (*os.File, error) {
		opened = true
		return nil, errors.New("injected open failure")
	}
	if hub.startRejoined(sessionKey(session), sub, at) {
		t.Fatal("rejoined despite the injected descriptor failure")
	}
	if !opened {
		t.Error("startRejoined bypassed the hub's open seam")
	}
	if hub.entryFor(file.path) != nil {
		t.Error("failed rejoin registered an entry")
	}
}

func TestFailedHandOverPublishesFailureAfterReaderStops(t *testing.T) {
	file := newTestFile(t)
	session := Session{Target: file.target(), FilePath: file.path, NewProcessor: (&recorder{}).newProcessor}
	seams := defaultSeams()
	seams.replaceTarget = func(*fs.ReadFile, fs.ValidatedReadTarget) error {
		return errors.New("injected hand-over failure")
	}
	e := newEntry(sessionKey(session), session, unknownPosition(), nil,
		Options{RetryInterval: time.Second}, logging.NopLogger{}, seams, func(*entry) {})
	sub := newSubscriber(session, defaultQueueChunks)
	if _, added := e.add(sub); !added {
		t.Fatal("subscriber was not added")
	}
	e.mu.Lock()
	e.handOver(sub)
	e.mu.Unlock()

	select {
	case it := <-sub.queue:
		t.Fatalf("published %+v before the reader stopped", it)
	case <-time.After(50 * time.Millisecond):
	}
	close(e.done) // Simulate the reader's deferred completion after its final flush.
	select {
	case it := <-sub.queue:
		if it.kind != failedItem || !errors.Is(it.err, ErrReaderFailed) {
			t.Errorf("last item = %+v, want a reader failure", it)
		}
	case <-time.After(waitTimeout):
		t.Fatal("the stopped reader's hand-over failure was not published")
	}
}
