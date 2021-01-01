package logger

type pause struct {
	pauseCh  chan struct{}
	resumeCh chan struct{}
}

// Notify that logger has to start pausing.
func (p pause) pause() {
	p.pauseCh <- struct{}{}
}

// Notify that logger can resume.
func (p pause) resume() {
	p.resumeCh <- struct{}{}
}

// Start waiting until pause is over.
func (p pause) start(done <-chan struct{}) <-chan struct{} {
	ch := make(chan struct{})

	go func() {
		select {
		case <-p.resumeCh:
		case <-done:
		}
		close(ch)
	}()

	return ch
}
