package discovery

import (
	"github.com/mimecast/dtail/internal/io/logger"
	"strings"
)

// ServerListFromCOMMA retrieves a list of servers from comma separated input list.
func (d *Discovery) ServerListFromCOMMA() []string {
	logger.Debug("Retrieving server list from comma separated list", d.server)
	return strings.Split(d.server, ",")
}
