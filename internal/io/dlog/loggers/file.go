package loggers

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

type fileMessageBuf struct {
	now     time.Time
	message string
	nl      bool
}

type file struct {
	bufferCh     chan *fileMessageBuf
	pauseCh      chan struct{}
	resumeCh     chan struct{}
	rotateCh     chan struct{}
	flushCh      chan struct{}
	fd           *os.File
	writer       *bufio.Writer
	mutex        sync.Mutex
	started      bool
	lastFileName string
	strategy     Strategy
}

var _ Logger = (*file)(nil)

func newFile(strategy Strategy) *file {
	// Control channels are buffered with capacity 1 and used with
	// non-blocking, coalescing sends so Pause/Resume/Rotate/Flush callers
	// never block on the logger goroutine (and repeated signals collapse
	// into a single pending notification).
	return &file{
		bufferCh: make(chan *fileMessageBuf, runtime.NumCPU()*100),
		pauseCh:  make(chan struct{}, 1),
		resumeCh: make(chan struct{}, 1),
		rotateCh: make(chan struct{}, 1),
		flushCh:  make(chan struct{}, 1),
		strategy: strategy,
	}
}

func (f *file) Start(ctx context.Context, wg *sync.WaitGroup) {
	f.mutex.Lock()
	defer func() {
		f.started = true
		f.mutex.Unlock()
	}()

	if f.started {
		// Logger already started from another Goroutine.
		wg.Done()
		return
	}

	pause := func(ctx context.Context) {
		select {
		case <-f.resumeCh:
			return
		case <-ctx.Done():
			return
		}
	}

	go func() {
		defer wg.Done()
		for {
			select {
			case m := <-f.bufferCh:
				f.write(m)
			case <-f.pauseCh:
				pause(ctx)
			case <-f.flushCh:
				f.flush()
			case <-f.rotateCh:
				// Force re-opening the outfile on the next write.
				// Drained here (not only from write()) so that Rotate()
				// makes progress even when no log messages arrive.
				f.lastFileName = ""
			case <-ctx.Done():
				f.flush()
				// f.fd is only populated after the first getWriter() call;
				// guard against a nil pointer when the logger is shut down
				// before anything has been written.
				if f.fd != nil {
					f.fd.Close()
				}
				return
			}
		}
	}()
}

func (f *file) Log(now time.Time, message string) {
	f.bufferCh <- &fileMessageBuf{now, message, true}
}

func (f *file) LogWithColors(now time.Time, message, coloredMessage string) {
	f.RawWithColors(now, message, coloredMessage)
}

func (f *file) Raw(now time.Time, message string) {
	f.bufferCh <- &fileMessageBuf{now, message, false}
}

func (f *file) RawWithColors(now time.Time, message, coloredMessage string) {
	panic("Colors not supported in file logger")
}

// signal performs a non-blocking, coalescing send on a capacity-1 control
// channel. If a signal is already pending the new one is dropped, which is
// the desired behaviour for idempotent operations such as Pause/Rotate/Flush.
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (f *file) Pause()  { signal(f.pauseCh) }
func (f *file) Resume() { signal(f.resumeCh) }
func (f *file) Flush()  { signal(f.flushCh) }
func (f *file) Rotate() { signal(f.rotateCh) }

func (*file) SupportsColors() bool { return false }

func (f *file) write(m *fileMessageBuf) {
	var writer *bufio.Writer
	if f.strategy.Rotation == DailyRotation {
		writer = f.getWriter(m.now.Format("20060102"))
	} else {
		writer = f.getWriter(f.strategy.FileBase)
	}

	// Don't report any error, we won't be able to log it anyway!
	_, _ = writer.WriteString(m.message)
	if m.nl {
		_ = writer.WriteByte('\n')
	}
}

func (f *file) getWriter(name string) *bufio.Writer {
	if f.lastFileName == name {
		return f.writer
	}
	if _, err := os.Stat(config.Common.LogDir); os.IsNotExist(err) {
		if err = os.MkdirAll(config.Common.LogDir, 0755); err != nil {
			panic(err)
		}
	}

	logFile := fmt.Sprintf("%s/%s.log", config.Common.LogDir, name)
	newFd, err := os.OpenFile(logFile, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}

	// Close old writer.
	if f.fd != nil {
		f.writer.Flush()
		f.fd.Close()
	}
	// Set new writer.
	f.fd = newFd
	f.writer = bufio.NewWriterSize(f.fd, 1)
	f.lastFileName = name

	return f.writer
}

func (f *file) flush() {
	defer func() {
		if f.writer != nil {
			f.writer.Flush()
		}
	}()
	for {
		select {
		case m := <-f.bufferCh:
			f.write(m)
		default:
			return
		}
	}
}
