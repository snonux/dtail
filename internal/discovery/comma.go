package discovery

import (
	"strings"

	"github.com/mimecast/dtail/internal/io/dlog"
)

// ServerListFromCOMMA retrieves a list of servers from comma separated input list.
func (d *Discovery) ServerListFromCOMMA() []string {
	dlog.Common.Debug("Retrieving server list from comma separated list", d.server)

	rawServers := strings.Split(d.server, ",")
	servers := make([]string, 0, len(rawServers))
	for _, server := range rawServers {
		if server == "" {
			continue
		}
		servers = append(servers, server)
	}
	if len(servers) == 0 && d.server == "" {
		return rawServers
	}

	return servers
}
