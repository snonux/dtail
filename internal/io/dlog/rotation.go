package dlog

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mimecast/dtail/internal/io/dlog/loggers"
)

func rotation(ctx context.Context) {
	rotateCh := make(chan os.Signal, 1)
	signal.Notify(rotateCh, syscall.SIGHUP)
	go rotateLoop(ctx, rotateCh, loggers.FactoryRotate)
}

// rotateLoop services the log-rotation channel until ctx is cancelled. It is
// split out from rotation so tests can drive it directly with a fake rotate
// function and a test-owned channel.
func rotateLoop(ctx context.Context, rotateCh <-chan os.Signal, rotate func()) {
	for {
		select {
		case <-rotateCh:
			Common.Debug("Invoking log rotation")
			rotate()
		case <-ctx.Done():
			return
		}
	}
}
