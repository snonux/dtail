package signal

import (
	"context"
	"os"
	gosignal "os/signal"
	"syscall"
	"time"

	"github.com/mimecast/dtail/internal/config"
)

// InterruptChWithCancel returns a channel for "please print stats" signalling.
// It accepts a cancel function to properly shutdown when termination signals are received.
func InterruptChWithCancel(ctx context.Context, cancel context.CancelFunc) <-chan string {
	statsCh, _ := startInterruptChWithCancel(ctx, cancel)
	return statsCh
}

// InterruptCh returns a channel for "please print stats" signalling.
//
// Deprecated: Use InterruptChWithCancel for proper cleanup on termination signals.
func InterruptCh(ctx context.Context) <-chan string {
	statsCh, _ := startInterruptCh(ctx)
	return statsCh
}

func startInterruptChWithCancel(ctx context.Context,
	cancel context.CancelFunc) (<-chan string, <-chan struct{}) {
	return startInterruptHandler(ctx, func() {
		cancel()
		// Wait longer to allow MapReduce cleanup, then force exit if still running.
		go forceExitAfter(time.After(5*time.Second), os.Exit)
	})
}

func startInterruptCh(ctx context.Context) (<-chan string, <-chan struct{}) {
	return startInterruptHandler(ctx, func() {
		forceExit(os.Exit)
	})
}

func startInterruptHandler(ctx context.Context, terminate func()) (<-chan string, <-chan struct{}) {
	sigIntCh := make(chan os.Signal, 10)
	gosignal.Notify(sigIntCh, os.Interrupt)
	sigOtherCh := make(chan os.Signal, 10)
	gosignal.Notify(sigOtherCh, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	statsCh := make(chan string)
	stoppedCh := make(chan struct{})

	go func() {
		defer close(stoppedCh)
		defer gosignal.Stop(sigIntCh)
		defer gosignal.Stop(sigOtherCh)

		runInterruptHandler(ctx, sigIntCh, sigOtherCh, statsCh, terminate)
	}()
	return statsCh, stoppedCh
}

func runInterruptHandler(ctx context.Context, sigIntCh, sigOtherCh <-chan os.Signal,
	statsCh chan<- string, terminate func()) {
	for {
		select {
		case <-sigIntCh:
			select {
			case statsCh <- "Hint: Hit Ctrl+C again to exit":
				if !waitForSecondInterrupt(ctx, sigIntCh, terminate) {
					return
				}
			default:
				// Stats already printed.
			}
		case <-sigOtherCh:
			terminate()
		case <-ctx.Done():
			return
		}
	}
}

func waitForSecondInterrupt(ctx context.Context, sigIntCh <-chan os.Signal,
	terminate func()) bool {
	timer := time.NewTimer(time.Second * time.Duration(config.InterruptTimeoutS))
	defer timer.Stop()

	select {
	case <-sigIntCh:
		terminate()
	case <-timer.C:
	case <-ctx.Done():
		return false
	}
	return true
}

func forceExitAfter(wait <-chan time.Time, exit func(int)) {
	<-wait
	forceExit(exit)
}

func forceExit(exit func(int)) {
	exit(1)
}

// NoCh doesn't listen on a signal.
func NoCh(ctx context.Context) <-chan string {
	return make(chan string)
}
