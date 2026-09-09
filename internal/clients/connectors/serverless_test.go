package connectors

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	serverHandlers "github.com/mimecast/dtail/internal/server/handlers"
	sessionspec "github.com/mimecast/dtail/internal/session"
)

func TestServerlessStartReturnsAfterCancellationAndDrainsServerOutput(t *testing.T) {
	resetClientLogger(t)

	clientHandler := newServerlessLifecycleClient()
	serverHandler := newServerlessLifecycleServer([]byte("final output"))
	connector := NewServerless(
		"test-user",
		clientHandler,
		nil,
		sessionspec.Spec{},
		false,
		serverlessLifecycleFactory{handler: serverHandler},
	)
	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		connector.Start(ctx, cancel, nil, nil)
	}()

	waitForSignal(t, clientHandler.readStarted, "client command read to start")
	waitForSignal(t, serverHandler.readStarted, "server output read to start")
	cancel()
	waitForSignal(t, startDone, "serverless connector to return after cancellation")
	waitForSignal(t, clientHandler.readFinished, "client command read to finish")

	if got, want := clientHandler.output(), "final output"; got != want {
		t.Fatalf("client output = %q, want %q", got, want)
	}
	if clientHandler.shutdownBeforeOutput() {
		t.Fatal("client handler shut down before the final server output was drained")
	}
	if got := clientHandler.shutdownCalls(); got != 1 {
		t.Fatalf("client handler Shutdown calls = %d, want 1", got)
	}
	if got := serverHandler.shutdownCalls(); got != 1 {
		t.Fatalf("server handler Shutdown calls = %d, want 1", got)
	}
	if got := serverHandler.gracefulShutdownCalls(); got != 1 {
		t.Fatalf("server handler GracefulShutdown calls = %d, want 1", got)
	}
}

func TestServerlessOutputFailureUsesAbruptShutdownAndReturnsError(t *testing.T) {
	resetClientLogger(t)

	wantErr := errors.New("client output failed")
	clientHandler := &failingServerlessClient{
		serverlessLifecycleClient: newServerlessLifecycleClient(),
		err:                       wantErr,
	}
	serverHandler := newEagerServerlessLifecycleServer([]byte("trigger output failure"))
	connector := NewServerless(
		"test-user",
		clientHandler,
		nil,
		sessionspec.Spec{},
		false,
		serverlessLifecycleFactory{handler: serverHandler},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- connector.handle(ctx, cancel)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("serverless handle error = %v, want %v", err, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serverless teardown blocked after its output consumer failed")
	}
	if got := serverHandler.gracefulShutdownCalls(); got != 0 {
		t.Fatalf("GracefulShutdown calls = %d, want abrupt shutdown", got)
	}
	if got := serverHandler.shutdownCalls(); got != 1 {
		t.Fatalf("Shutdown calls = %d, want 1", got)
	}
}

type serverlessLifecycleFactory struct {
	handler serverHandlers.Handler
}

func (f serverlessLifecycleFactory) NewServerlessHandler(string) (serverHandlers.Handler, error) {
	return f.handler, nil
}

type serverlessLifecycleClient struct {
	done             chan struct{}
	doneOnce         sync.Once
	readStarted      chan struct{}
	readStartedOnce  sync.Once
	readFinished     chan struct{}
	readFinishedOnce sync.Once
	mu               sync.Mutex
	received         []byte
	shutdownCount    int
	shutdownWasEarly bool
}

func newServerlessLifecycleClient() *serverlessLifecycleClient {
	return &serverlessLifecycleClient{
		done:         make(chan struct{}),
		readStarted:  make(chan struct{}),
		readFinished: make(chan struct{}),
	}
}

var _ handlers.Handler = (*serverlessLifecycleClient)(nil)

func (*serverlessLifecycleClient) Capabilities() []string                 { return nil }
func (*serverlessLifecycleClient) HasCapability(string) bool              { return false }
func (*serverlessLifecycleClient) ReportServerError(string)               {}
func (*serverlessLifecycleClient) SendMessage(string) error               { return nil }
func (*serverlessLifecycleClient) Server() string                         { return "lifecycle" }
func (*serverlessLifecycleClient) Status() int                            { return 0 }
func (h *serverlessLifecycleClient) Done() <-chan struct{}                { return h.done }
func (*serverlessLifecycleClient) WaitForCapabilities(time.Duration) bool { return false }
func (*serverlessLifecycleClient) WaitForSessionAck(time.Duration) (handlers.SessionAck, bool) {
	return handlers.SessionAck{}, false
}

func (h *serverlessLifecycleClient) Read([]byte) (int, error) {
	h.readStartedOnce.Do(func() { close(h.readStarted) })
	<-h.done
	h.readFinishedOnce.Do(func() { close(h.readFinished) })
	return 0, io.EOF
}

func (h *serverlessLifecycleClient) Write(p []byte) (int, error) {
	h.mu.Lock()
	h.received = append(h.received, p...)
	h.mu.Unlock()
	return len(p), nil
}

func (h *serverlessLifecycleClient) Shutdown() {
	h.mu.Lock()
	h.shutdownCount++
	h.shutdownWasEarly = len(h.received) == 0
	h.mu.Unlock()
	h.doneOnce.Do(func() { close(h.done) })
}

func (h *serverlessLifecycleClient) output() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return string(h.received)
}

func (h *serverlessLifecycleClient) shutdownCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shutdownCount
}

func (h *serverlessLifecycleClient) shutdownBeforeOutput() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shutdownWasEarly
}

type serverlessLifecycleServer struct {
	payload         []byte
	shutdown        chan struct{}
	shutdownOnce    sync.Once
	readStarted     chan struct{}
	readStartedOnce sync.Once
	mu              sync.Mutex
	shutdownCount   int
	gracefulCount   int
	eager           bool
}

func newServerlessLifecycleServer(payload []byte) *serverlessLifecycleServer {
	return &serverlessLifecycleServer{
		payload:     payload,
		shutdown:    make(chan struct{}),
		readStarted: make(chan struct{}),
	}
}

func newEagerServerlessLifecycleServer(payload []byte) *serverlessLifecycleServer {
	h := newServerlessLifecycleServer(payload)
	h.eager = true
	return h
}

var _ serverHandlers.Handler = (*serverlessLifecycleServer)(nil)

func (h *serverlessLifecycleServer) Read(p []byte) (int, error) {
	h.readStartedOnce.Do(func() { close(h.readStarted) })
	if !h.eager || len(h.payload) == 0 {
		<-h.shutdown
	}
	if len(h.payload) == 0 {
		return 0, io.EOF
	}
	n := copy(p, h.payload)
	h.payload = h.payload[n:]
	return n, nil
}

func (*serverlessLifecycleServer) Write(p []byte) (int, error) { return len(p), nil }

func (h *serverlessLifecycleServer) Shutdown() {
	h.mu.Lock()
	h.shutdownCount++
	h.mu.Unlock()
	h.shutdownOnce.Do(func() { close(h.shutdown) })
}

func (h *serverlessLifecycleServer) GracefulShutdown() {
	h.mu.Lock()
	h.gracefulCount++
	h.mu.Unlock()
	h.Shutdown()
}

func (h *serverlessLifecycleServer) GracefulShutdownContext(context.Context) {
	h.GracefulShutdown()
}

func (h *serverlessLifecycleServer) Done() <-chan struct{} { return h.shutdown }

func (h *serverlessLifecycleServer) shutdownCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shutdownCount
}

func (h *serverlessLifecycleServer) gracefulShutdownCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gracefulCount
}

type failingServerlessClient struct {
	*serverlessLifecycleClient
	err error
}

func (h *failingServerlessClient) Write([]byte) (int, error) {
	return 0, h.err
}
