package clients

import (
	"github.com/mimecast/dtail/internal/clients/handlers"
)

// maker is a factory interface that enables code reuse across all DTail client
// implementations. While all clients share the baseClient structure for common
// functionality like connection management and SSH authentication, each client
// type requires specialized handlers and commands for their specific operations.
//
// This interface allows baseClient to create the appropriate components for
// each client type without knowing the specific implementation details, following
// the factory pattern to maintain separation of concerns.
//
// Implementation requirements:
// - Each client type must implement both methods
// - makeHandler should return a handler appropriate for the client's operations
// - makeCommands should generate protocol commands for the client's mode
type maker interface {
	// makeHandler creates a connection handler appropriate for this client type.
	// The handler manages protocol communication, result processing, and status
	// reporting for the specific server.
	//
	// Parameters:
	//   server: The server hostname/address for this handler
	//
	// Returns:
	//   handlers.Handler: A handler configured for this client type and server
	makeHandler(server string) handlers.Handler

	// makeCommands generates the appropriate DTail protocol commands for this
	// client type. The commands are sent to DTail servers to initiate the
	// desired operations (tail, cat, grep, map, health, etc.).
	//
	// Returns:
	//   []string: List of protocol commands to send to servers
	makeCommands() (commands []string)
}
