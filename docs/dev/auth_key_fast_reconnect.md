# Auth-Key Fast Reconnect: Design and Implementation

Developer notes. The user guide (what it is, flags, configuration, security)
is [doc/auth-key-fast-reconnect.md](../../doc/auth-key-fast-reconnect.md).

## Design principles

1. **Transparent fallback.** The client offers its keys through a single
   `gossh.PublicKeys` auth method whose signers are ordered so the local key
   is tried first. If the fast key is rejected, the SSH library continues
   with the next signers (configured fallback keys, then the agent/YubiKey)
   without any user interaction.
2. **Server keys are ephemeral.** The in-memory store is lost on server
   restart. No file I/O, no persistence.
3. **Trust chain preserved.** An auth key can only be registered over a
   session that was already authenticated the normal way.
4. **Minimal protocol addition.** A single `AUTHKEY <base64-pubkey>` command
   over the existing SSH session text protocol.

## Overview

```
┌─────────────────────────────────────────────────────────┐
│ DTail Client                                            │
│                                                         │
│  One gossh.PublicKeys auth method, signers tried in     │
│  order:                                                 │
│    1. Auth-key path (default ~/.ssh/id_rsa)  ← FAST     │
│    2. Config fallback paths                             │
│    3. SSH agent (YubiKey)                               │
│    4. Further default key paths                         │
│                                                         │
│  After the session is established (and before the       │
│  DTail commands are dispatched):                        │
│    → reads <authKeyPath>.pub                            │
│    → sends AUTHKEY <base64-pubkey> to server            │
└────────────────────────┬────────────────────────────────┘
                         │ SSH
┌────────────────────────▼────────────────────────────────┐
│ DTail Server (dserver)                                  │
│                                                         │
│  PublicKeyCallback:                                     │
│    1. Check in-memory authkeystore  ← FAST (only when   │
│       AuthKeyEnabled is set)                            │
│    2. Check authorized_keys        ← existing path      │
│                                                         │
│  AUTHKEY command handler (only inside an authenticated  │
│  session):                                              │
│    → authkey.Store.Add(user, pubkey)                    │
│    → responds AUTHKEY OK / AUTHKEY ERR <reason>         │
│                                                         │
│  authkey.Store (in-memory only):                        │
│    map[username] → []PublicKey (with TTL, max per user) │
└─────────────────────────────────────────────────────────┘
```

## Sequences

### First connection (slow path, YubiKey)

1. The client collects signers: the key at `--auth-key-path` (default
   `~/.ssh/id_rsa`) first, then configured fallback paths, then the SSH
   agent, then the remaining default key paths, all in one
   `gossh.PublicKeys` auth method.
2. The SSH handshake begins; the server's `PublicKeyCallback` is called with
   the local key.
3. The server checks the in-memory auth-key store: not found.
4. The server checks `authorized_keys` (or the per-user authorized-keys
   cache): not found, this key is not in there.
5. The server rejects the key; the SSH library tries the next signers, which
   reach the SSH agent (YubiKey).
6. The YubiKey signs the challenge; the server finds the YubiKey public key
   in `authorized_keys` and the authentication succeeds.
7. Once the session is established, and before any DTail command is
   dispatched, the client reads `<authKeyPath>.pub` and sends
   `AUTHKEY <base64-pubkey>`.
8. The server's handler parses the command, calls
   `authkey.Store.Add(user, pubkey)` and answers `AUTHKEY OK`.

The registration is sent on every connection (unless the feature is
disabled). Re-registering a known key is idempotent: the server drops the old
entry and adds the key again with a fresh TTL.

### Subsequent connections (fast path, no YubiKey)

1. The client offers the same signers, local key first.
2. The server's `PublicKeyCallback` finds the key in the in-memory store and
   the authentication succeeds at once.
3. No YubiKey touch is needed.

### Fallback (server restarted, key expired)

1. The client offers the local key; the store is empty or the entry expired,
   so the key is rejected.
2. The client's auth method continues with the next signer; the YubiKey
   authentication succeeds.
3. The client registers the local public key again with `AUTHKEY`, so the
   next connection is fast again.

## Components

### Server: in-memory store (`internal/authkey/authkeystore.go`)

`authkey.Store` is a thread-safe, per-user cache of SSH public keys: a
`map[string][]authKeyEntry` guarded by a mutex, one entry per registered key
with its `gossh.PublicKey` and registration time. Each `dserver` instance
owns exactly one store; there is no package-level singleton and no disk
persistence.

* `Add(user, pubKey)` removes a duplicate entry of the same key and adds the
  key with a fresh TTL. Beyond the per-user limit the oldest entries are
  dropped.
* `Has(user, pubKey)` is true only for a key that has not expired.
* `Remove(user, pubKey)` deletes one key.
* Expiry is lazy: `Add`, `Has` and `Remove` prune the user's expired entries
  first. There is no background reaper.
* A TTL or per-user limit of 0 or less uses the default (24 h, 5 keys).

### Server: PublicKeyCallback (`internal/ssh/server/publickeycallback.go`)

`NewPublicKeyCallback` wires the store into the SSH handshake. The callback
refuses invalid user names and the password-only users (`DTAIL-HEALTH`,
`DTAIL-SCHEDULE`, `DTAIL-CONTINUOUS`) first.

With `AuthKeyEnabled` it then
checks the in-memory store; a hit authorizes the connection at once
(`Authorized by in-memory auth key store`). A miss falls through to the
file-based check: the configured `AuthorizedKeysPath`, else the per-user
cache `<CacheDir>/<user>.authorized_keys`, else `~/.ssh/authorized_keys`.
With the feature disabled only that file-based check runs.

### Server: AUTHKEY command handler (`internal/handlers/serverhandler.go`)

`handleAuthKeyCommand` accepts `AUTHKEY <base64-pubkey>` only within an
authenticated session and answers `AUTHKEY OK` or `AUTHKEY ERR <reason>`:

| Reason | Cause |
| --- | --- |
| `feature disabled` | `Server.AuthKeyEnabled` is `false`. |
| `unsupported user` | The session user is a password-only user. |
| `missing public key` | No key was sent. |
| `invalid base64` | The payload does not decode. |
| `invalid public key` | The decoded blob is no SSH public key. |
| `internal key store unavailable` | The server has no store wired up. |

Both replies are marked control frames (see
[wire_framing.md](./wire_framing.md)), so a payload line reading
`AUTHKEY OK` is never taken for one.

### Client: signer order (`internal/ssh/client/authmethods.go`)

The client uses one `gossh.AuthMethod`, `gossh.PublicKeys(signers...)`
(`collectKnownHostsSigners`), with the signers in this order:

1. the auth-key path (`--auth-key-path`, default `~/.ssh/id_rsa`),
2. further configured fallback key paths; they come before the agent so that
   an agent with many keys can not use up the server's authentication
   attempts before a known-good key is tried,
3. all SSH agent keys, or the one chosen with `--agentKeyIndex` (a YubiKey is
   typically exposed here),
4. the default key paths `~/.ssh/id_rsa`, `~/.ssh/id_dsa`, `~/.ssh/id_ecdsa`
   and `~/.ssh/id_ed25519`.

Duplicate keys are offered once. As all signers are in one auth method, Go's
SSH client moves to the next signer when one is rejected: that is the
transparent fallback.

### Client: registration (`internal/clients/connectors/serverconnection.go`)

`registerAuthKey` runs on every connection once the SSH session is up and
before the DTail commands are dispatched, unless the feature is disabled.
`sendAuthKeyRegistrationCommand` reads `<authKeyPath>.pub`, takes the base64
key blob of its first line that is neither blank nor a `#` comment and sends
`AUTHKEY <base64>`.

The client handler consumes the reply and never prints it
(`internal/clients/handlers/basehandler.go`): `AUTHKEY OK` is logged at
DEBUG, `AUTHKEY ERR <reason>` as the WARN `AUTHKEY registration failed`.
Registration is best effort; a failure does not affect the session. A
missing, unreadable or invalid `.pub` file, or an auth-key path that could
not be resolved at all, is logged at DEBUG and skipped.

### Configuration plumbing

* Server keys: `internal/config/server.go` (`AuthKeyEnabled`,
  `AuthKeyTTLSeconds`, `AuthKeyMaxPerUser`).
* Client flags: `internal/cli/authkeyflags.go` (`--auth-key-path`, the
  deprecated `--key`) and `internal/cli/client.go` (`--no-auth-key`).
* Client keys: `internal/config/client.go` (`AuthKeyPath`, `AuthKeyDisable`;
  `defaultAuthKeyPath` builds `<home>/.ssh/id_rsa`).
* Environment and precedence: `internal/config/initializer.go`
  (`resolveSSHKeyPath`, `setupConfig`).

### dtailhealth

`dtailhealth` never registers an auth key (`clients.NewHealthClient` sets
`NoAuthKey`): the health user authenticates by password and `dserver` refuses
keys for it. It still accepts `--no-auth-key`, without effect, and has no
`--auth-key-path` flag (`cmd/dtailhealth/main.go`).

History: older `dtailhealth` binaries sent `AUTHKEY` before their health
command whenever the auth key's `.pub` file existed. The health handler took
that for an unknown command and ended the session, so a healthy server was
reported CRITICAL unless `--no-auth-key` was given.

Both sides are fixed and
either is enough: a current `dtailhealth` works against an older protocol 5
`dserver`, and a current `dserver` ignores `AUTHKEY` in health sessions (no
reply, no registration, `HealthHandler.ignoreAuthKey`).
