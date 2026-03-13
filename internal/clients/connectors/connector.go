package connectors

import (
	"context"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
)

// Connector interface.
type Connector interface {
	// Start the connection.
	Start(ctx context.Context, cancel context.CancelFunc, throttleCh, statsCh chan struct{})
	// Server hostname.
	Server() string
	// Handler for the connection.
	Handler() handlers.Handler
	// SupportsQueryUpdates reports whether the connected server advertised
	// runtime query replacement support within the given timeout.
	SupportsQueryUpdates(timeout time.Duration) bool
}
