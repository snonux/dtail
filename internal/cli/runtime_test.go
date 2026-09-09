package cli

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/profiling"
	"github.com/mimecast/dtail/internal/source"
)

func TestClientRuntimeKeepsLoggerAliveUntilStop(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	loggerCtxCh := make(chan context.Context, 1)
	loggerStopped := make(chan struct{})
	loggerMessages := make(chan string, 1)
	var logged string
	startLogger := func(ctx context.Context, wg *sync.WaitGroup, process source.Source) {
		if process != source.Client {
			t.Errorf("logger process = %v, want client", process)
		}
		loggerCtxCh <- ctx
		go func() {
			defer wg.Done()
			defer close(loggerStopped)
			for {
				select {
				case message := <-loggerMessages:
					logged += message
				case <-ctx.Done():
					for {
						select {
						case message := <-loggerMessages:
							logged += message
						default:
							return
						}
					}
				}
			}
		}()
	}
	runtime := newClientRuntime(parent, profiling.Flags{}, "test", startLogger)
	loggerCtx := <-loggerCtxCh

	cancelParent()
	<-runtime.Context().Done()
	if err := loggerCtx.Err(); err != nil {
		t.Fatalf("logger stopped with work context: %v", err)
	}

	loggerMessages <- "final aggregate"
	runtime.Stop()
	select {
	case <-loggerStopped:
	default:
		t.Fatal("runtime Stop returned before logger shutdown completed")
	}
	if logged != "final aggregate" {
		t.Fatalf("post-cancellation logger output = %q, want final aggregate", logged)
	}
}

func TestClientRuntimeStopShutsDownPProf(t *testing.T) {
	prevClient := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Client = prevClient
	})

	ctx, cancel := context.WithCancel(context.Background())
	runtime := &ClientRuntime{
		ctx:      ctx,
		cancel:   cancel,
		profiler: profiling.NewProfiler(profiling.Config{}),
	}

	runtime.StartPProf("127.0.0.1:0")
	if runtime.pprofServer == nil {
		t.Fatal("expected pprof server to start")
	}

	url := "http://" + runtime.pprofServer.Address() + "/debug/pprof/"
	waitForHTTPStatus(t, url, http.StatusOK)

	runtime.Stop()
	waitForHTTPError(t, url)
}

func waitForHTTPStatus(t *testing.T, url string, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s to return %d", url, want)
}

func waitForHTTPError(t *testing.T, url string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			return
		}
		resp.Body.Close()
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s to stop serving", url)
}
