package connectors

import (
	"context"
	"io"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/io/dlog"
	serverHandlers "github.com/mimecast/dtail/internal/server/handlers"
	sessionspec "github.com/mimecast/dtail/internal/session"
)

// ServerlessHandlerFactory creates the in-process server-side handler used by serverless mode.
type ServerlessHandlerFactory interface {
	NewServerlessHandler(userName string) (serverHandlers.Handler, error)
}

// Serverless creates a server object directly without TCP.
type Serverless struct {
	handler        handlers.Handler
	commands       []string
	sessionSpec    sessionspec.Spec
	sessionState   committedSessionState
	interactive    bool
	userName       string
	handlerFactory ServerlessHandlerFactory
}

var _ Connector = (*Serverless)(nil)

// NewServerless starts a new serverless session.
func NewServerless(userName string, handler handlers.Handler,
	commands []string, sessionSpec sessionspec.Spec, interactive bool,
	handlerFactory ServerlessHandlerFactory) *Serverless {

	dlog.Client.Debug("Creating new serverless connector", handler, commands)
	return &Serverless{
		userName:       userName,
		handler:        handler,
		commands:       commands,
		sessionSpec:    sessionSpec,
		interactive:    interactive,
		handlerFactory: handlerFactory,
	}
}

// Server returns serverless server indicator.
func (s *Serverless) Server() string {
	return "local(serverless)"
}

// Handler returns the handler used for the serverless connection.
func (s *Serverless) Handler() handlers.Handler {
	return s.handler
}

// SupportsQueryUpdates reports whether the in-process server advertised
// runtime query update support to the client handler.
func (s *Serverless) SupportsQueryUpdates(timeout time.Duration) bool {
	return supportsQueryUpdates(s.handler, timeout)
}

// ApplySessionSpec starts or updates the in-process interactive session state.
func (s *Serverless) ApplySessionSpec(spec sessionspec.Spec, timeout time.Duration) error {
	return applySessionSpec(s.Server(), s.handler, &s.sessionState, spec, timeout)
}

// CommittedSession returns the last server-acknowledged session state.
func (s *Serverless) CommittedSession() (sessionspec.Spec, uint64, bool) {
	return s.sessionState.snapshot()
}

// Start the serverless connection.
func (s *Serverless) Start(ctx context.Context, cancel context.CancelFunc,
	throttleCh, statsCh chan struct{}) {

	dlog.Client.Debug("Starting serverless connector")
	go func() {
		defer cancel()
		if err := s.handle(ctx, cancel); err != nil {
			dlog.Client.Warn(err)
		}
	}()
	<-ctx.Done()
}

func (s *Serverless) handle(ctx context.Context, cancel context.CancelFunc) error {
	dlog.Client.Debug("Creating server handler for a serverless session")

	if s.handlerFactory == nil {
		return io.ErrClosedPipe
	}
	serverHandler, err := s.handlerFactory.NewServerlessHandler(s.userName)
	if err != nil {
		return err
	}

	terminate := func() {
		dlog.Client.Debug("Terminating serverless connection")
		serverHandler.Shutdown()
		cancel()
	}

	// Use buffered channels to prevent deadlock
	// This approach avoids the circular dependency of direct io.Copy

	// Channels for data flow
	toServer := make(chan []byte, 100)
	fromServer := make(chan []byte, 100)

	// Error tracking
	errChan := make(chan error, 4)

	// Read from client handler
	go func() {
		defer close(toServer)
		buf := make([]byte, 32*1024)
		for {
			n, err := s.handler.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case toServer <- data:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					errChan <- err
				}
				return
			}
		}
	}()

	// Write to server handler
	go func() {
		for data := range toServer {
			if _, err := serverHandler.Write(data); err != nil {
				errChan <- err
				return
			}
		}
	}()

	// Read from server handler
	go func() {
		defer close(fromServer)
		buf := make([]byte, 64*1024) // Larger buffer for server responses
		for {
			n, err := serverHandler.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case fromServer <- data:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					errChan <- err
				}
				return
			}
		}
	}()

	// Write to client handler
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for data := range fromServer {
			if _, err := s.handler.Write(data); err != nil {
				errChan <- err
				return
			}
		}
	}()

	if err := dispatchInitialCommands(s.Server(), s.handler, s.commands, s.interactive, s.sessionSpec, &s.sessionState); err != nil {
		return err
	}

	// Monitor for completion
	go func() {
		defer terminate()
		select {
		case <-s.handler.Done():
			dlog.Client.Trace("<-s.handler.Done()")
		case <-serverDone:
			dlog.Client.Trace("Server transfer done")
		case <-ctx.Done():
			dlog.Client.Trace("<-ctx.Done()")
		}
	}()

	// Wait for completion
	<-ctx.Done()

	// Check for errors
	select {
	case err := <-errChan:
		return err
	default:
	}

	s.handler.Shutdown()
	return nil
}
