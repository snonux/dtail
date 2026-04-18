package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"sync"

	"github.com/mimecast/dtail/internal/io/dlog"
)

// PProfServer owns a dedicated pprof HTTP server lifecycle.
type PProfServer struct {
	listener net.Listener
	server   *http.Server
	done     chan struct{}
}

// NewPProfServer creates a pprof HTTP server bound to address.
func NewPProfServer(address string) (*PProfServer, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}

	return &PProfServer{
		listener: listener,
		server: &http.Server{
			Handler: newPProfServeMux(),
		},
		done: make(chan struct{}),
	}, nil
}

// Address returns the bound pprof listener address.
func (s *PProfServer) Address() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Start serves the pprof HTTP endpoints until shutdown.
func (s *PProfServer) Start(wg *sync.WaitGroup) {
	if s == nil {
		return
	}

	if wg != nil {
		wg.Add(1)
	}

	go func() {
		if wg != nil {
			defer wg.Done()
		}
		defer close(s.done)

		if err := s.server.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			dlog.Client.Error("PProf server exited", err)
		}
	}()
}

// Shutdown stops the pprof HTTP server and waits for Serve to return.
func (s *PProfServer) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}

	err := s.server.Shutdown(ctx)
	<-s.done
	return err
}

func newPProfServeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		mux.Handle("/debug/pprof/"+name, pprof.Handler(name))
	}

	return mux
}
