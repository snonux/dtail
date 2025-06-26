package connectors

import (
	"context"
	"io"
	"sync"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	serverHandlers "github.com/mimecast/dtail/internal/server/handlers"
	user "github.com/mimecast/dtail/internal/user/server"
)

// Serverless creates a server object directly without TCP.
type Serverless struct {
	handler  handlers.Handler
	commands []string
	userName string
}

// NewServerless starts a new serverless session.
func NewServerless(userName string, handler handlers.Handler,
	commands []string) *Serverless {

	dlog.Client.Debug("Creating new serverless connector", handler, commands)
	return &Serverless{
		userName: userName,
		handler:  handler,
		commands: commands,
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

	user, err := user.New(s.userName, s.Server())
	if err != nil {
		return err
	}

	var serverHandler serverHandlers.Handler
	switch s.userName {
	case config.HealthUser:
		dlog.Client.Debug("Creating serverless health handler")
		serverHandler = serverHandlers.NewHealthHandler(user)
	default:
		dlog.Client.Debug("Creating serverless server handler")
		serverHandler = serverHandlers.NewServerHandler(
			user,
			make(chan struct{}, config.Server.MaxConcurrentCats),
			make(chan struct{}, config.Server.MaxConcurrentTails),
		)
	}

	terminate := func() {
		dlog.Client.Debug("Terminating serverless connection")
		serverHandler.Shutdown()
		cancel()
	}

	// Create a sync.WaitGroup to track goroutines
	var wg sync.WaitGroup
	wg.Add(2)
	
	// Use channels to prevent deadlock
	const bufferSize = 32 * 1024  // Smaller chunks for better flow
	fromClient := make(chan []byte, 100)  // Larger channel buffer
	fromServer := make(chan []byte, 100)  // Larger channel buffer
	
	// Goroutine 1: Read from client handler, send to channel
	go func() {
		defer wg.Done()
		defer close(fromClient)
		
		buf := make([]byte, bufferSize)
		for {
			n, err := s.handler.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case fromClient <- data:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					dlog.Client.Trace("Read from handler error:", err)
				}
				return
			}
		}
	}()
	
	// Goroutine 2: Read from server handler, send to channel
	go func() {
		defer wg.Done()
		defer close(fromServer)
		
		buf := make([]byte, bufferSize)
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
					dlog.Client.Trace("Read from serverHandler error:", err)
				}
				return
			}
		}
	}()
	
	// Goroutine 3: Write from client to server
	go func() {
		for data := range fromClient {
			if _, err := serverHandler.Write(data); err != nil {
				dlog.Client.Trace("Write to serverHandler error:", err)
				terminate()
				return
			}
		}
	}()
	
	// Goroutine 4: Write from server to client
	go func() {
		for data := range fromServer {
			if _, err := s.handler.Write(data); err != nil {
				dlog.Client.Trace("Write to handler error:", err)
				terminate()
				return
			}
		}
	}()
	
	// Goroutine 5: Monitor for completion
	go func() {
		defer terminate()
		select {
		case <-s.handler.Done():
			dlog.Client.Trace("<-s.handler.Done()")
		case <-ctx.Done():
			dlog.Client.Trace("<-ctx.Done()")
		}
	}()

	// Send all commands to server
	for _, command := range s.commands {
		dlog.Client.Debug("Sending command to serverless server", command)
		if err := s.handler.SendMessage(command); err != nil {
			dlog.Client.Debug(err)
		}
	}

	// Wait for context to be done
	<-ctx.Done()
	
	// Shutdown handlers
	dlog.Client.Trace("s.handler.Shutdown()")
	s.handler.Shutdown()
	
	// Wait for goroutines to finish
	wg.Wait()
	
	return nil
}
