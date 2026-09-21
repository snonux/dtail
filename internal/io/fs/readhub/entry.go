package readhub

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mimecast/dtail/internal/ctxutil"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/omode"
)

// entry is the shared follow read of one file.
type entry struct {
	key     entryKey
	path    string
	options Options
	logger  logging.Logger
	// onFailure lets the hub drop an entry whose reader failed for good.
	onFailure func(*entry)
	failOnce  sync.Once
	// seams holds the operations tests replace; see hubSeams.
	seams hubSeams

	reader   *fs.ReadFile
	messages chan string
	cancel   context.CancelFunc
	ctx      context.Context
	// done is closed once the reader returned and the file is closed.
	done chan struct{}

	// mu guards the subscriber list and the owner; the hub's mutex is always
	// taken first when both are needed.
	mu          sync.Mutex
	subscribers []*subscriber
	// owner is the subscriber whose target the reader opens the file with.
	owner *subscriber

	// publishMu keeps every subscriber's queue in the order items were
	// published by the reader and the message forwarder.
	publishMu sync.Mutex
}

func newEntry(key entryKey, creator Session, options Options, logger logging.Logger,
	seams hubSeams, onFailure func(*entry)) *entry {

	ctx, cancel := context.WithCancel(context.Background())
	e := &entry{
		key:       key,
		path:      creator.FilePath,
		options:   options,
		logger:    logger,
		onFailure: onFailure,
		seams:     seams,
		messages:  make(chan string),
		cancel:    cancel,
		ctx:       ctx,
		done:      make(chan struct{}),
	}
	target := creator.Target
	reader, err := fs.NewReadFile(fs.ReadOptions{
		Mode:           omode.TailClient,
		Target:         &target,
		FilePath:       creator.FilePath,
		GlobID:         creator.GlobID,
		ServerMessages: e.messages,
		SeekEOF:        true,
		MaxLineLength:  options.MaxLineLength,
		Logger:         logger,
	})
	if err != nil {
		// NewReadFile only fails for an unsupported mode or target kind, and
		// both are fixed or validated before an entry is made.
		panic(fmt.Sprintf("readhub: shared follow reader: %v", err))
	}
	e.reader = reader
	return e
}

// start runs the reader and the message forwarder.
func (e *entry) start() {
	e.logger.Info(e.path, "Shared follow read started", "subscribers=1")
	go e.forwardMessages()
	go e.run()
}

// stop ends the read once the last subscriber left.
func (e *entry) stop() {
	e.logger.Info(e.path, "Shared follow read stopped", "subscribers=0")
	e.cancel()
}

// add registers sub; the first subscriber owns the reader's target.
func (e *entry) add(sub *subscriber) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subscribers = append(e.subscribers, sub)
	if e.owner == nil {
		e.owner = sub
	}
	if count := len(e.subscribers); count > 1 {
		e.logger.Info(e.path, "Shared follow read gained a subscriber", fmt.Sprintf("subscribers=%d", count))
	}
}

// remove unregisters sub, stops deliveries to it and returns how many
// subscribers remain. When sub owned the reader's target, the reader is
// handed the target of the longest-subscribed remaining session.
func (e *entry) remove(sub *subscriber) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, candidate := range e.subscribers {
		if candidate == sub {
			e.subscribers = append(e.subscribers[:i], e.subscribers[i+1:]...)
			break
		}
	}
	close(sub.done)

	remaining := len(e.subscribers)
	if remaining > 0 {
		e.logger.Info(e.path, "Shared follow read lost a subscriber", fmt.Sprintf("subscribers=%d", remaining))
	}
	if e.owner == sub {
		e.owner = nil
		if remaining > 0 {
			e.handOver(e.subscribers[0])
		}
	}
	return remaining
}

// handOver makes the reader use next's target. The caller holds e.mu.
func (e *entry) handOver(next *subscriber) {
	if err := e.seams.replaceTarget(e.reader, next.session.Target); err != nil {
		// Every subscriber's target resolves to the entry's path, so this
		// cannot fail; stop rather than keep reading through a departed
		// session's target. fail takes the hub's and the entry's locks,
		// which the caller holds.
		e.cancel()
		go e.fail(fmt.Errorf("%w: hand over the shared read target: %w", fs.ErrReaderWorkerPanic, err))
		return
	}
	e.owner = next
}

// fail ends the shared read for good: the hub forgets the entry, so the next
// session starts a new reader, and every subscriber's Follow returns err. err
// wraps fs.ErrReaderWorkerPanic, which a private read reports for a reader
// that panicked, so callers handle both alike.
func (e *entry) fail(err error) {
	e.failOnce.Do(func() {
		e.logger.Error(e.path, err)
		e.onFailure(e)
		e.cancel()
		e.publish(item{kind: failedItem, err: err})
	})
}

// recoverPanic turns a panic on one of the entry's goroutines into a failed
// shared read: the private reader runs on the session's goroutine, where a
// panic ends that session only, so a shared one must not crash dserver.
func (e *entry) recoverPanic(where string) {
	if recovered := recover(); recovered != nil {
		e.fail(fmt.Errorf("%w: shared %s: %v", fs.ErrReaderWorkerPanic, where, recovered))
	}
}

// run is the shared equivalent of the read command's retry loop for one
// file: it starts the reader again after a read ended, e.g. after the file
// was rotated, and announces each new read to the subscribers, who then make
// a new processor as a private read would.
func (e *entry) run() {
	defer close(e.done)
	defer e.recoverPanic("reader")
	fanout := newFanoutProcessor(e)

	for iteration := 0; ; iteration++ {
		if iteration > 0 {
			e.publish(item{kind: reopenItem})
		}
		err := e.seams.startReader(e.ctx, e.reader, fanout)
		fanout.publishPending()
		if errors.Is(err, fs.ErrReaderWorkerPanic) {
			e.fail(err)
			return
		}
		if err != nil {
			e.logger.Error(e.path, err)
		}
		if e.ctx.Err() != nil || !ctxutil.Sleep(e.ctx, e.options.RetryInterval) {
			return
		}
		e.logger.Info(e.path, "Reading file again")
	}
}

// forwardMessages tells every subscriber when the reader warns about a long
// line, the only message a reader sends to its client. The subscriber words
// the warning with its own file path.
func (e *entry) forwardMessages() {
	defer e.recoverPanic("message forwarder")
	for {
		select {
		case <-e.messages:
			e.publish(item{kind: longLineItem})
		case <-e.ctx.Done():
			return
		}
	}
}

// publish delivers it to every current subscriber, in publication order. A
// subscriber that joins later does not get it. Until slow subscribers are
// evicted, publish waits for a subscriber whose queue is full.
func (e *entry) publish(it item) {
	e.publishMu.Lock()
	defer e.publishMu.Unlock()
	for _, sub := range e.snapshot() {
		sub.deliver(it)
	}
}

func (e *entry) snapshot() []*subscriber {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*subscriber(nil), e.subscribers...)
}

// ownerSession returns the session whose target the reader uses, for tests.
func (e *entry) ownerSession() (Session, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.owner == nil {
		return Session{}, false
	}
	return e.owner.session, true
}
