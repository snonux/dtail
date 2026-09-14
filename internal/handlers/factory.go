package handlers

import (
	"io"

	"github.com/mimecast/dtail/internal/authkey"
	"github.com/mimecast/dtail/internal/config"
	user "github.com/mimecast/dtail/internal/sessionuser"
)

// Dependencies contains the process-owned resources used by a session handler.
type Dependencies struct {
	ServerConfig     *config.ServerConfig
	CatLimiter       chan struct{}
	TailLimiter      chan struct{}
	AuthKeyStore     *authkey.Store
	ServerlessOutput io.Writer
	Loggers          HandlerLoggers
	Capabilities     []string
}

// NewForUser creates the handler appropriate for the authenticated user.
func NewForUser(user *user.User, dependencies Dependencies) (Handler, error) {
	if user != nil && user.Name == config.HealthUser {
		return NewHealthHandler(user, dependencies.ServerConfig, dependencies.Loggers.Diagnostics)
	}
	return NewServerHandler(user, dependencies)
}
