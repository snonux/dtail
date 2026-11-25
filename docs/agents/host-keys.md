# Host Key Verification

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

The client checks server host keys against `known_hosts`
(`internal/ssh/client/knownhostscallback.go`). An unknown host is asked about
on the terminal, or trusted and added without asking with `--trustAllHosts`.
A host whose key differs from the one in `known_hosts` is not unknown: it is
refused with a `HOST KEY CHANGED` error in every mode, unless the client was
started with `--trustChangedHostKeys` (`Args.TrustChangedHostKeys`, all
clients and `dtailhealth`, which otherwise trusts all hosts). With that flag
the prompt names the hosts whose key changed, and trust-all mode replaces
their entries with a WARN. A key marked `@revoked` is never trusted. The
parsed file is cached until it changes (file identity, size, modification
time). Scheduled and continuous jobs authenticate with their own auth methods
and do not use `known_hosts`.

**Connect and handshake timeouts:** `Common.SSHConnectTimeoutMs` (default
2000) bounds only the TCP connect (`net.Dialer.Timeout` in
`ServerConnection.dial`). `Common.SSHHandshakeTimeoutMs` (default 30000)
bounds the SSH handshake after it (`ssh.NewClientConn`, which has no timeout
of its own) with a deadline on the TCP connection (`handshakeDeadline`,
`internal/clients/connectors/handshake_deadline.go`), so a host that accepts
TCP but never completes the handshake fails the connection (`no handshake
within 30s`) instead of leaving it connecting forever. The host key callback
pauses the deadline and resumes it with the time left, so the unknown host
question may wait for the user's answer as long as it takes; the deadline is
cleared once the handshake ended, and re-keys later leave it alone. Every
connection of every client, `dtailhealth` and the scheduler's jobs goes
through `ServerConnection`, so it applies to all of them. Agent signing is
not paused: a touch- or confirm-per-signature agent key (hardware key,
`ssh-add -c`) shared by many connections can run later ones into the
deadline.
