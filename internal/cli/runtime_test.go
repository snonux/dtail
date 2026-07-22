package cli

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/profiling"
)

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
