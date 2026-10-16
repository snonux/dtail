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
// client before it connected to any server. Whitespace around the file path
// is ignored as well (see serverListFilePath). A file with no server entry
// yields an empty list, which the client rejects unless it runs serverless.
func (d *Discovery) ServerListFromFILE() (servers []string, retErr error) {
	path := serverListFilePath(d.server)
	d.logger.Debug("Retrieving server list from file", path)

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open server discovery file %q: %w", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close server discovery file %q: %w", path, err)
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
		return nil, fmt.Errorf("scan server discovery file %q: %w", path, err)
	}

	return servers, nil
}
