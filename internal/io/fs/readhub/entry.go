package readhub

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mimecast/dtail/internal/ctxutil"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// entry is the shared follow read of one file.
type entry struct {
	key     entryKey
	path    string
	options Options
	logger  logging.Logger
	// onFailure lets the hub drop an entry whose reader failed for good.
	onFailure func(*entry)

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
	onFailure func(*entry)) *entry {

	ctx, cancel := context.WithCancel(context.Background())
	e := &entry{
		key:       key,
		path:      creator.FilePath,
		options:   options,
		logger:    logger,
		onFailure: onFailure,
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
	if err := e.reader.ReplaceTarget(next.session.Target); err != nil {
		// Every subscriber's target resolves to the entry's path, so this
		// cannot fail; stop rather than keep reading through a departed
		// session's target.
		e.logger.Error(e.path, "Unable to hand over the shared read target", err)
		e.cancel()
		return
	}
	e.owner = next
}

// run is the shared equivalent of the read command's retry loop for one
// file: it starts the reader again after a read ended, e.g. after the file
// was rotated, and announces each new read to the subscribers, who then make
// a new processor as a private read would.
func (e *entry) run() {
	defer close(e.done)
	fanout := newFanoutProcessor(e)

	for iteration := 0; ; iteration++ {
		if iteration > 0 {
			e.publish(item{kind: reopenItem})
		}
		err := e.reader.Start(e.ctx, lcontext.LContext{}, fanout, regex.NewNoop())
		fanout.publishPending()
		if err != nil {
			e.logger.Error(e.path, err)
			if errors.Is(err, fs.ErrReaderWorkerPanic) {
				e.onFailure(e)
				e.publish(item{kind: failedItem, err: err})
				e.cancel()
				return
			}
		}
		if e.ctx.Err() != nil || !ctxutil.Sleep(e.ctx, e.options.RetryInterval) {
			return
		}
		e.logger.Info(e.path, "Reading file again")
	}
}

// forwardMessages passes the reader's messages for the client, such as the
// long line warning, on to every subscriber.
func (e *entry) forwardMessages() {
	for {
		select {
		case message := <-e.messages:
			e.publish(item{kind: messageItem, message: message})
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
