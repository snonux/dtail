// Package clients provides the client-side implementation for DTail's distributed
// log processing system. This package contains all client types that connect to
// DTail servers over SSH to perform distributed operations like tailing, grepping,
// and MapReduce aggregations on log files across multiple servers.
//
// The package implements a common client architecture where all clients inherit
// from baseClient and implement the Client interface. Clients can operate in
// either server mode (connecting via SSH) or serverless mode (local operations).
//
// Key client types:
//   - TailClient: Continuously monitors log files for new content
//   - CatClient: Retrieves complete file contents from start to end
//   - GrepClient: Searches files for lines matching regular expressions
//   - MaprClient: Performs distributed MapReduce operations with SQL-like queries
//   - HealthClient: Performs basic health checks on DTail servers
package clients

import "context"

// Client is the main interface that all DTail clients must implement.
// It provides a standardized way to start client operations with proper
// context management and statistics reporting.
type Client interface {
	// Start initiates the client operation with the provided context and
	// statistics channel. The context allows for graceful cancellation,
	// while the statistics channel enables real-time monitoring of client
	// operations such as connection counts and data transfer rates.
	//
	// Parameters:
	//   ctx: Context for cancellation and timeout control
	//   statsCh: Channel for receiving statistics display requests
	//
	// Returns:
	//   int: Exit status code (0 for success, non-zero for various error conditions)
	Start(ctx context.Context, statsCh <-chan string) int
}
