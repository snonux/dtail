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
	sigIntCh := make(chan os.Signal, 10)
	gosignal.Notify(sigIntCh, os.Interrupt)
	sigOtherCh := make(chan os.Signal, 10)
	gosignal.Notify(sigOtherCh, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	statsCh := make(chan string)

	go func() {
		for {
			select {
			case <-sigIntCh:
				select {
				case statsCh <- "Hint: Hit Ctrl+C again to exit":
					select {
					case <-sigIntCh:
						cancel()
						// Wait longer to allow MapReduce cleanup, then force exit if still running
						go func() {
							time.Sleep(5 * time.Second)
							os.Exit(0)
						}()
					case <-time.After(time.Second * time.Duration(config.InterruptTimeoutS)):
					}
				default:
					// Stats already printed.
				}
			case <-sigOtherCh:
				// Cancel context to allow graceful shutdown (MapReduce outfile cleanup, etc.)
				cancel()
				// Wait longer to allow MapReduce cleanup, then force exit if still running
				go func() {
					time.Sleep(5 * time.Second)
					os.Exit(0)
				}()
			case <-ctx.Done():
				return
			}
		}
	}()
	return statsCh
}

// InterruptCh returns a channel for "please print stats" signalling.
// Deprecated: Use InterruptChWithCancel for proper cleanup on termination signals.
func InterruptCh(ctx context.Context) <-chan string {
	sigIntCh := make(chan os.Signal, 10)
	gosignal.Notify(sigIntCh, os.Interrupt)
	sigOtherCh := make(chan os.Signal, 10)
	gosignal.Notify(sigOtherCh, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	statsCh := make(chan string)

	go func() {
		for {
			select {
			case <-sigIntCh:
				select {
				case statsCh <- "Hint: Hit Ctrl+C again to exit":
					select {
					case <-sigIntCh:
						os.Exit(0)
					case <-time.After(time.Second * time.Duration(config.InterruptTimeoutS)):
					}
				default:
					// Stats already printed.
				}
			case <-sigOtherCh:
				os.Exit(0)
			case <-ctx.Done():
				return
			}
		}
	}()
	return statsCh
}

// NoCh doesn't listen on a signal.
func NoCh(ctx context.Context) <-chan string {
	return make(chan string)
}
