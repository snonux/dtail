package discovery

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// serverFileCommentPrefix starts a comment line in a server list file.
const serverFileCommentPrefix = "#"

// ServerListFromFILE retrieves a list of servers from a file, one server per
// line. Each line is trimmed (see trimServerEntry), and blank lines as well as
// lines starting with '#' after trimming are skipped. Without that, a trailing
// blank line, an empty separator line or an indented entry became an empty or
// whitespace-wrapped address, which the client rejects, aborting the whole
// client before it connected to any server.
func (d *Discovery) ServerListFromFILE() (servers []string, retErr error) {
	d.logger.Debug("Retrieving server list from file", d.server)

	file, err := os.Open(d.server)
	if err != nil {
		return nil, fmt.Errorf("open server discovery file %q: %w", d.server, err)
	}
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close server discovery file %q: %w", d.server, err)
		}
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		server, ok := trimServerEntry(scanner.Text())
		if !ok || strings.HasPrefix(server, serverFileCommentPrefix) {
			continue
		}
		servers = append(servers, server)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan server discovery file %q: %w", d.server, err)
	}

	return servers, nil
}
