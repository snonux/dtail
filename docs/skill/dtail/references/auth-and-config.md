# Authentication and client configuration

## Config file

Without `--cfg`, clients read `~/.config/dtail/dtail.conf`, else
`~/.dtail.conf` (first found wins, never merged); `--cfg none` reads none.
The JSON file has `Common`, `Server` and `Client` sections. Client-relevant
keys:

| Key | Meaning |
| --- | --- |
| `Common.SSHPort` | Default server port (2222). |
| `Common.SSHConnectTimeoutMs` | TCP connect timeout (default 2000). |
| `Common.SSHHandshakeTimeoutMs` | SSH handshake timeout (default 30000); time spent on the unknown-host question does not count, agent signing does. Fails with `no handshake within 30s`. |
| `Common.Logger`, `Common.LogDir`, `Common.LogLevel` | Logger defaults (flags win). |
| `Common.HostnameOverride` | Hostname shown in output. |
| `Client.KnownHostsPath` | `known_hosts` file. |
| `Client.AuthKeyPath`, `Client.AuthKeyDisable` | Fast reconnect key / off switch. |
| `Client.LogPayload` | Tee payload into the daily log file. |
| `Client.EditMode` | `emacs` (default) or `vi` line editing. |
| `Client.TermColorsEnable`, `Client.TermColors` (incl. `.Interactive`) | Colors; unset keys keep defaults. |

Schema and a commented example: `examples/dtail.schema.json`,
`examples/dtail.json.example`. `Server.Permissions` in the client's config
also applies to serverless reads. `dtailhealth` reads the user's default
config but ignores `Common.Logger`/`Common.LogDir`.

## SSH authentication

- The client authenticates as the local user (or `--user`) with the SSH
  agent (`SSH_AUTH_SOCK`; `--agentKeyIndex N` picks one key, `-1` tries
  all) and/or a private key file.
- Private key path precedence: `--auth-key-path` > `DTAIL_AUTH_KEY_PATH` >
  legacy `DTAIL_SSH_PRIVATE_KEYFILE_PATH` > `Client.AuthKeyPath` >
  `~/.ssh/id_rsa`. Auth order: that key (fast reconnect), configured
  fallback key paths, the SSH agent, then the remaining default keys
  (`id_rsa`, `id_dsa`, `id_ecdsa`, `id_ed25519`).
- An encrypted key needs `DTAIL_KEY_PASSPHRASE`; there is no passphrase
  prompt.
- The server authorizes the key from the user's `~/.ssh/authorized_keys`
  (or `Server.AuthorizedKeysPath`, or the key cache filled by
  `update_key_cache.sh`; see [dserver-setup.md](dserver-setup.md)). Test
  with plain `ssh` first when in doubt.

## Auth-key fast reconnect (on by default)

After a normal SSH login the client sends `AUTHKEY <pubkey>` (the `.pub`
next to the auth key path). `dserver` keeps it in memory per user
(`Server.AuthKeyTTLSeconds` 86400, `Server.AuthKeyMaxPerUser` 5) and
accepts it on later connections before trying `authorized_keys`, so a
hardware key (YubiKey) is not touched on every reconnect. A server restart,
expiry or mismatch falls back to normal auth. Disable with `--no-auth-key`
or `Client.AuthKeyDisable: true`; server side `Server.AuthKeyEnabled:
false`. A cached key stays valid until TTL or restart even if the original
key is revoked. `dtailhealth` never registers a key.

## Host keys

- Unknown hosts are collected and asked about in one terminal question
  (`y=yes,a=all,n=no,d=details`), about 2 s after the last handshake, then
  written to `known_hosts` (`--known-hosts-path` or
  `Client.KnownHostsPath`; default `~/.ssh/known_hosts`).
- `--trustAllHosts`: trust and add unknown hosts without asking (no 2 s
  wait). Only when the user accepts that decision.
- A **changed** key is refused with `HOST KEY CHANGED` in every mode,
  including `--trustAllHosts`; remove the stale `known_hosts` entry, or add
  `--trustChangedHostKeys` (all clients and `dtailhealth`) to accept and
  replace it. `@revoked` keys are never trusted.
- Scheduled/continuous jobs use their own auth and no `known_hosts`.
- `dtailhealth` trusts unknown host keys by itself (changed keys are still
  refused).

## Environment variables

| Variable | Use |
| --- | --- |
| `DTAIL_AUTH_KEY_PATH` | Private key path (beats the legacy one). |
| `DTAIL_SSH_PRIVATE_KEYFILE_PATH` | Legacy key path alias. |
| `DTAIL_KEY_PASSPHRASE` | Passphrase of an encrypted key. |
| `DTAIL_HOSTNAME_OVERRIDE` | Fallback for `--hostname-override`. |
| `DTAIL_INTEGRATION_TEST_RUN_MODE=yes` | Test harness mode only (see [development.md](development.md)). |
| `VISUAL`, `EDITOR` | Editor for Alt-E in interactive mode. |
