package logger

import (
	"bufio"
	"fmt"
	"os"
	"sync"

	"github.com/mimecast/dtail/internal/config"
)

type outfile struct {
	pause
	filePath string
	bufCh    chan buf
	rotateCh chan struct{}
	fd       *os.File
	writer   *bufio.Writer
	dateStr  string
	mutex    sync.Mutex
	done     chan struct{}
}

func newOutfile(filePath string, bufLen int) outfile {
	return outfile{
		pause: pause{
			pauseCh:  make(chan struct{}),
			resumeCh: make(chan struct{}),
		},
		filePath: filePath,
		bufCh:    make(chan buf, bufLen),
		rotateCh: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

func (o outfile) start() {
	for {
		select {
		case buf := <-o.bufCh:
			o.fileWriter(buf.time.Format("20060102")).WriteString(buf.message)
		case <-o.pause.start(o.done):
		case <-o.done:
			return
		}
	}
}

func (o outfile) stop() {
	close(o.done)
	o.closeFileWriter()
}

func (o outfile) fileWriter(dateStr string) *bufio.Writer {
	select {
	case <-o.rotateCh:
		return o.updateFileWriter(dateStr)
	default:
		if dateStr != o.dateStr {
			return o.updateFileWriter(dateStr)
		}
		return o.writer
	}
}

func (o outfile) updateFileWriter(dateStr string) *bufio.Writer {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	o.closeFileWriter()

	if _, err := os.Stat(config.Common.LogDir); os.IsNotExist(err) {
		if err = os.MkdirAll(config.Common.LogDir, 0755); err != nil {
			panic(err)
		}
	}

	logFile := fmt.Sprintf("%s/%s.log", config.Common.LogDir, dateStr)
	newFd, err := os.OpenFile(logFile, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		panic(err)
	}

	o.fd = newFd
	o.writer = bufio.NewWriterSize(o.fd, 1)
	o.dateStr = dateStr

	return o.writer
}

func (o outfile) closeFileWriter() {
	if o.writer != nil {
		o.writer.Flush()
		o.fd.Close()
	}
}

func (o outfile) rotate() {
	select {
	case o.rotateCh <- struct{}{}:
	default:
	}
}
