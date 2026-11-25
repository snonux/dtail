# Auth-Key Fast Reconnect

## What it is

With a YubiKey (or another key that needs a touch), every DTail connection
costs one touch, and many servers at once are signed one after the other.

Auth-key fast reconnect avoids that after the first connection:

1. The first connection authenticates the normal way (agent, YubiKey).
2. Over that session the client registers a local public key with `dserver`,
   which keeps it **in memory only**.
3. Later connections offer the local key first and are accepted without a
   touch.
4. When `dserver` does not know the key (restart, expiry), the client falls
   back to the normal authentication by itself and registers the key again.

The local key does not need to be in `authorized_keys`.

## How to use it

It is on by default on both sides. All a client needs is a local key pair
without a touch requirement, by default `~/.ssh/id_rsa` and
`~/.ssh/id_rsa.pub`:

```shell
ssh-keygen -t rsa -f ~/.ssh/id_rsa   # once, if there is none
dtail --servers server1,server2 --files /var/log/app.log   # touch once per server
dtail --servers server1,server2 --files /var/log/app.log   # no touch
```

A key file with a passphrase is only loaded when `DTAIL_KEY_PASSPHRASE`
holds the passphrase.

Another key:

```shell
dcat --auth-key-path ~/.ssh/dtail_fast --servers server1 --files /etc/hostname
```

Without the `.pub` file next to the key nothing is registered; the client
only says so at debug log level.

To turn it off: `--no-auth-key` on a client, or `Server.AuthKeyEnabled:
false` on a `dserver`.

## Client options

| Flag | Config key | Default | Meaning |
| --- | --- | --- | --- |
| `--auth-key-path FILE` | `Client.AuthKeyPath` | `~/.ssh/id_rsa` | Private key offered first; `FILE.pub` is registered. |
| `--key FILE` | | | Deprecated alias of `--auth-key-path`, which wins when both are given (with a warning). |
| `--no-auth-key` | `Client.AuthKeyDisable` | `false` | Turn the feature off. |

| Environment variable | Meaning |
| --- | --- |
| `DTAIL_AUTH_KEY_PATH` | Auth-key path. |
| `DTAIL_SSH_PRIVATE_KEYFILE_PATH` | Legacy name, used only when `DTAIL_AUTH_KEY_PATH` is not set. |

Precedence of the path, highest first: the flag, `DTAIL_AUTH_KEY_PATH`,
`DTAIL_SSH_PRIVATE_KEYFILE_PATH`, `Client.AuthKeyPath`, `~/.ssh/id_rsa`.

When no path can be determined (no home directory and nothing configured),
the client disables the feature with a warning; set `DTAIL_AUTH_KEY_PATH`.

`dtailhealth` never registers a key. It accepts `--no-auth-key` without
effect, so existing check scripts keep working.

## Server options

| Key | Default | Meaning |
| --- | --- | --- |
| `Server.AuthKeyEnabled` | `true` | Accept key registrations and cached keys. |
| `Server.AuthKeyTTLSeconds` | `86400` (24 h) | Lifetime of a cached key; registering it again renews it. `0` or less: the default. |
| `Server.AuthKeyMaxPerUser` | `5` | Cached keys per user; the oldest is dropped beyond that. `0` or less: the default. |

```json
{
  "Server": {
    "AuthKeyEnabled": true,
    "AuthKeyTTLSeconds": 86400,
    "AuthKeyMaxPerUser": 5
  }
}
```

The job users (`DTAIL-SCHEDULE`, `DTAIL-CONTINUOUS`) and the health user
(`DTAIL-HEALTH`) authenticate by password and can not use auth keys.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| Every connection still needs a touch | No `FILE.pub` next to the auth key, the feature is off on one side, or `dserver` restarted in between. |
| `AUTHKEY registration failed` (client WARN), reason `feature disabled` | `Server.AuthKeyEnabled` is `false` on that server. |
| A touch again after a day | The cached key expired (`AuthKeyTTLSeconds`); the next connection is fast again. |

## Security notes

* **Memory only.** `dserver` never writes a cached key to disk; a restart
  drops them all.
* **Trust chain.** A key can only be registered over a session that already
  authenticated the normal way.
* **It is a delegated credential.** Whoever holds the private key can
  connect as that user to every `dserver` that still caches it, and read
  what that user may read there. Protect the key file (mode `0600`).
* **Revocation window.** Removing the user's normal key from
  `authorized_keys` does not remove a cached auth key. It stays valid until
  it expires (24 h by default) or `dserver` restarts.
* **Bounded.** At most `AuthKeyMaxPerUser` keys per user.

In effect the feature adds the key to the user's `authorized_keys` for the
TTL. Choose a TTL that fits your revocation window, or turn the feature off
where that is not acceptable.

## See also

* Design, sequences and source files:
  [docs/dev/auth_key_fast_reconnect.md](../docs/dev/auth_key_fast_reconnect.md)
* [Server Configuration Guide](./server-configuration.md)
