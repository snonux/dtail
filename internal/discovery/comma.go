package discovery

import "strings"

// ServerListFromCOMMA retrieves a list of servers from comma separated input
// list. Each entry is trimmed and empty entries are skipped (see
// trimServerEntry), so "a, b" and "a,,b," both yield [a b]. Only an empty
// input yields the serverless sentinel [""]: the client then opens a single
// serverless session instead of connecting to any server.
func (d *Discovery) ServerListFromCOMMA() ([]string, error) {
	d.logger.Debug("Retrieving server list from comma separated list", d.server)

	if d.server == "" {
		return []string{""}, nil
	}
	rawServers := strings.Split(d.server, ",")
	servers := make([]string, 0, len(rawServers))
	for _, raw := range rawServers {
		if server, ok := trimServerEntry(raw); ok {
			servers = append(servers, server)
		}
	}

	return servers, nil
}
