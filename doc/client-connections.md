Client Connections
==================

How a DTail client (`dtail`, `dcat`, `dgrep`, `dmap`, `dtailhealth`) finds its
config, authenticates, checks host keys, times out and logs. For a first setup
see the [Quick Starting Guide](quickstart.md).

# Config file

| Flag | File read |
|------|-----------|
| `--cfg PATH` | `PATH` |
| `--cfg none` | none |
| no `--cfg` | `~/.config/dtail/dtail.conf` if it exists, else `~/.dtail.conf` |

The file is JSON. The first file that exists wins; the two are not merged.
`dtailhealth` has no `--cfg` flag and uses the default lookup. The settings are
described in the [Server Configuration Guide](server-configuration.md).

# SSH keys

A client offers its keys in this order: the `--auth-key-path` key (default
`~/.ssh/id_rsa`), the SSH agent's keys (`SSH_AUTH_SOCK`), then `~/.ssh/id_dsa`,
`~/.ssh/id_ecdsa` and `~/.ssh/id_ed25519`. Details and the fast reconnect:
[auth-key-fast-reconnect.md](auth-key-fast-reconnect.md).

| Setting | Meaning |
|---------|---------|
| `--auth-key-path FILE` | Private key file (`--key` is a deprecated alias) |
| `DTAIL_AUTH_KEY_PATH` | Same, as environment variable |
| `DTAIL_SSH_PRIVATE_KEYFILE_PATH` | Legacy variable, used only when `DTAIL_AUTH_KEY_PATH` is unset |
| `Client.AuthKeyPath` | Same, in the config file |
| `--agentKeyIndex N` | Use only the agent's key at the 0-based index `N` (default `-1`: all keys) |
| `--user NAME` | SSH user name (default: your system user) |
| `DTAIL_KEY_PASSPHRASE` | Passphrase of a password-protected key file |

The flag wins over the environment variables. There is no passphrase prompt:
for a protected key file use `DTAIL_KEY_PASSPHRASE` or the SSH agent.

# Host key verification

The client checks every server's host key against `~/.ssh/known_hosts`.
`--known-hosts-path FILE` (or `Client.KnownHostsPath`) names another file; its
directory is created when missing.

| Host | Default | With flag |
|------|---------|-----------|
| Unknown | Asked on the terminal (`y=yes,a=all,n=no,d=details`) | `--trustAllHosts`: trusted and added without asking |
| Key differs from `known_hosts` | Refused with `HOST KEY CHANGED`, also with `--trustAllHosts` | `--trustChangedHostKeys`: asked, or with `--trustAllHosts` replaced with a `HOST KEY CHANGED` warning |
| Key marked `@revoked` | Never trusted | Never trusted |

A changed key means a reinstalled server or someone in the middle. To connect
again without `--trustChangedHostKeys`, remove the host's entry from
`known_hosts`.

Timing:

* Unknown hosts whose handshakes arrive close together are asked about in one
  question, about 2 seconds after the last of them, or once 50 are pending.
* With `--trustAllHosts`, or after answering `a`, they are collected for about
  20 milliseconds only, so scripts and CI runs are not slowed down.
* Writing `known_hosts` can wait up to 5 seconds while another DTail client
  holds its lock.

In an interactive session, the line typed while the question is asked answers
it and is not taken as a control command.

`dtailhealth` trusts all unknown hosts and takes `--trustChangedHostKeys`.
The scheduled and continuous jobs of `dserver` do not use `known_hosts`.

# Connect and handshake timeouts

| Setting | Default | Bounds |
|---------|---------|--------|
| `Common.SSHConnectTimeoutMs` | `2000` | The TCP connect |
| `Common.SSHHandshakeTimeoutMs` | `30000` | The SSH handshake after it (version and key exchange, authentication) |

Both apply to every client, `dtailhealth` and the jobs of `dserver`.

* A host that accepts the TCP connection but never completes the handshake
  fails with `no handshake within 30s`.
* The host key check does not count: the unknown host question may wait for
  your answer.
* SSH agent signing counts. With a key that needs a touch or a confirmation
  per signature (a hardware key, `ssh-add -c`) and many servers, confirm
  promptly or raise `Common.SSHHandshakeTimeoutMs`.

# Client logs

| Logger (`--logger`) | Diagnostics go to | Payload goes to |
|---------------------|-------------------|-----------------|
| `fout` (default) | stdout and `~/log/YYYYMMDD.log` | stdout; the file too with `--log-payload` |
| `stdout` | stdout | stdout |
| `file` | the daily file | the daily file |
| `none` | nowhere | nowhere |

* Diagnostics are the client's own `INFO`/`WARN`/`ERROR` lines; payload is the
  retrieved file content.
* `--logDir DIR` (or `Common.LogDir`) sets the directory, `--logLevel` the
  level, `Client.LogPayload` is the config form of `--log-payload`.
* In serverless mode the payload is written to stdout directly, whatever the
  logger, also with `--logger none`.
* A flag wins over `Common.Logger`/`Common.LogDir` in the config file.
* `dtailhealth` ignores both config settings and logs nowhere unless
  `--logger` is given.

More on output and statuses: [examples.md](examples.md).
