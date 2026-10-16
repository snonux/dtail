package discovery

import "strings"

// ServerListFromCOMMA retrieves a list of servers from comma separated input
// list. Each entry is trimmed and empty entries are skipped (see
// trimServerEntry), so "a, b" and "a,,b," both yield [a b]. Only an empty
// input yields the sentinel [""], one entry naming no server. Without a
// discovery module the client then runs serverless (config sets
// Args.Serverless for an empty --servers without --discovery, see
// transformClient) and opens a single serverless session for that entry.
// With a discovery module the client is not serverless, and the entry ""
// fails as an empty server address. Input of only whitespace and commas
// yields an empty list, which the client rejects as no server to connect to.
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
