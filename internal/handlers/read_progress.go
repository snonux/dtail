package handlers

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

// progressReportInterval is how often a session reports its read progress
// while it changes.
const progressReportInterval = time.Second

// readProgress tracks how far the snapshot reads (cat and grep mode, and so
// map) of a session got, and reports it to the client as hidden
// protocol.HiddenProgressPrefix frames.
//
// Each file a read command will read is registered as soon as the session's
// permission check of it passed (readCommand.readFileIfPermissions), with the
// size its validation saw, so the total is complete right away even while
// files wait for a cat slot. The file's reader then counts the bytes it
// consumes (fs.ReadOptions.Progress, the compressed bytes of a compressed
// file) and corrects the size at every open.
//
// A file may grow while it is read (a log still written to): the read goes on
// to the end it finds, so every report tick stats the files being read
// through their open descriptors (fileProgress.refreshSize; only files
// holding a cat slot, as only those are open) and the total grows with them.
// A file that is read past its last known size counts with the bytes read as
// its size. When a read ends, however (to EOF, at --max, cancelled or
// failed), its file counts as done in full, so the progress ends at 100%.
//
// The progress covers one session generation: a read command of a newer one
// (a SESSION UPDATE) starts it over, and one of an older one is not tracked.
// A plain command stream has generation 0 throughout, so all its read
// commands add up.
//
// A goroutine started with the first registration posts a frame every
// progressReportInterval while the numbers changed, and readCommand.readFiles
// posts one when its files are done. Posting replaces a frame not sent yet
// (outputCoordinator.postProgress), so the client always gets the latest
// numbers and the session output never queues them.
type readProgress struct {
	mu         sync.Mutex
	generation uint64
	files      []*fileProgress
	// lastPosted is what was posted last, to post only changes.
	lastPosted protocol.Progress
	posted     bool
	started    bool
	// post hands an encoded, generation-tagged frame to the session output.
	post func(message string)
	// done ends the reporting goroutine with the session.
	done <-chan struct{}
}

// fileProgress is the progress of one file of a session's reads. It is the
// fs.ByteCounter its reader counts into.
type fileProgress struct {
	size     atomic.Int64
	read     atomic.Int64
	finished atomic.Bool
	// open is the file the reader has open, to follow its size while it is
	// read (refreshSize); nil before the open and after the read ended.
	open atomic.Pointer[os.File]
}

var _ fs.ByteCounter = (*fileProgress)(nil)

// Opened takes the file as just opened, its size (which may have changed
// since its validation) and starts the bytes read over, as the read does.
func (f *fileProgress) Opened(file *os.File) {
	f.open.Store(file)
	f.read.Store(0)
	f.refreshSize()
}

// refreshSize takes the current size of the file being read, if one is open
// and its read did not end. It stats the open descriptor, not the path, so a
// file rotated away meanwhile is still the one measured. A failing Stat (the
// reader closed the file meanwhile) changes nothing.
func (f *fileProgress) refreshSize() {
	file := f.open.Load()
	if file == nil || f.finished.Load() {
		return
	}
	if info, err := file.Stat(); err == nil {
		f.size.Store(info.Size())
	}
}

// Add counts n bytes read.
func (f *fileProgress) Add(n int64) {
	f.read.Add(n)
}

// finish marks the file's read as ended: it counts as read in full, at the
// larger of its last known size and the bytes read.
func (f *fileProgress) finish() {
	if f.finished.Swap(true) {
		return
	}
	f.open.Store(nil)
	if size := f.size.Load(); f.read.Load() < size {
		f.read.Store(size)
	}
}

func newReadProgress(post func(string), done <-chan struct{}) *readProgress {
	return &readProgress{post: post, done: done}
}

// register adds a file of size bytes read by a command of generation and
// returns its counter. A file of an older generation than the tracked one
// gets a counter that is not tracked. It returns nil for a nil readProgress.
func (p *readProgress) register(generation uint64, size int64) *fileProgress {
	if p == nil {
		return nil
	}
	file := &fileProgress{}
	file.size.Store(size)

	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case generation < p.generation:
		return file
	case generation > p.generation:
		p.generation = generation
		p.files = nil
		p.posted = false
	}
	p.files = append(p.files, file)
	if !p.started {
		p.started = true
		go p.report()
	}
	return file
}

// finish marks file's read as ended. readCommand.readFiles flushes once all
// files of its command ended, so that the client gets the final numbers
// before the session's close handshake.
func (p *readProgress) finish(file *fileProgress) {
	if p == nil || file == nil {
		return
	}
	file.finish()
}

// flush posts the current progress, if it changed since it was last posted.
func (p *readProgress) flush() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.postLocked()
}

// report posts the progress every progressReportInterval until the session
// ends.
func (p *readProgress) report() {
	ticker := time.NewTicker(progressReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.flush()
		case <-p.done:
			return
		}
	}
}

// postLocked posts the current progress if it changed. Snapshot and post
// happen under p.mu, so a later post never carries older numbers.
func (p *readProgress) postLocked() {
	if len(p.files) == 0 {
		return
	}
	current := p.snapshotLocked()
	if p.posted && current == p.lastPosted {
		return
	}
	p.lastPosted = current
	p.posted = true
	p.post(encodeGeneratedMessage(p.generation, protocol.EncodeProgress(current)))
}

// snapshotLocked sums the files' progress, taking the current size of the
// files being read first, as they may grow. A file read past its known size
// (it grew since it was last measured) counts with what was read as its size.
func (p *readProgress) snapshotLocked() protocol.Progress {
	var progress protocol.Progress
	for _, file := range p.files {
		file.refreshSize()
		size, read := file.size.Load(), file.read.Load()
		size = max(size, read)
		if file.finished.Load() {
			// Done in full, also should a refreshSize racing with the
			// finish have raised the size after it.
			read = size
			progress.Files++
		}
		progress.Bytes += read
		progress.Total += size
		progress.FilesTotal++
	}
	return progress
}

// counter returns f as the fs.ByteCounter of a reader, or nil (not an
// interface holding a nil pointer) for a nil f.
func (f *fileProgress) counter() fs.ByteCounter {
	if f == nil {
		return nil
	}
	return f
}

// trackProgress registers the file target with the session's read progress
// and returns its counter, or nil when the read is not tracked: a follow read
// (tail), whose end is unknown, a journal target, whose size is unknown, or
// a session without progress tracking. The size is the file's as of now; the
// reader corrects it at its open.
func (r *readCommand) trackProgress(target *fs.ValidatedReadTarget) *fileProgress {
	if r.progress == nil || target.Kind != fs.FileKind ||
		(r.mode != omode.CatClient && r.mode != omode.GrepClient) {
		return nil
	}
	var size int64
	if info, err := os.Stat(target.ResolvedPath()); err == nil {
		size = info.Size()
	}
	return r.progress.register(r.generation, size)
}
