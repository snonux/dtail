package logger

import (
	"bufio"
	"os"
)

type termout struct {
	pause
	writer *bufio.Writer
	bufCh  chan string
	done   chan struct{}
}

func newTermout(bufLen int) termout {
	return termout{
		pause: pause{
			pauseCh:  make(chan struct{}),
			resumeCh: make(chan struct{}),
		},
		writer: bufio.NewWriter(os.Stdout),
		bufCh:  make(chan string, bufLen),
		done:   make(chan struct{}),
	}
}

func (o termout) start() {
	for {
		select {
		case buf := <-o.bufCh:
			o.writer.WriteString(buf)
		case <-o.pause.start(o.done):
		case <-o.done:
			return
		}
	}
}

func (o termout) stop() {
	close(o.done)
	o.writer.Flush()
}
