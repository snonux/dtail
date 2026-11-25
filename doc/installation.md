DTail Installation Guide
========================

This guide sets up `dserver` as a `systemd` service on a Linux host. The
package examples use `dnf` (RHEL, Rocky Linux and Fedora); adapt them for your
platform. To only try DTail out, the [Quick Starting Guide](quickstart.md) is
enough. Upgrading an existing installation? Read the
[Upgrade Notes](upgrade-notes.md) first.

# Compile it

You need at least the Go version declared in [`go.mod`](../go.mod) (currently
Go 1.26.6) and a C compiler (the zstd support uses cgo). In a clone of the
repository:

```console
% make build
```

This compiles `dserver`, `dcat`, `dgrep`, `dmap`, `dtail`, `dtailhealth` and
the developer tool `dtail-tools` into the repository root. `make install` runs
`go install` for the six product binaries (not `dtail-tools`) and puts them
into `$GOBIN` instead. For a fleet, build a package (.rpm, .deb, ...) in your
build pipeline.

The build switches below are environment variables read by the Makefile. Any
non-empty value enables them (also `no`).

## Linux ACL support

Optional, for better security: `dserver` can honour file system access control
lists on Linux.

```console
% sudo dnf install libacl-devel -y
% DTAIL_USE_ACL=yes make build
```

Without the Makefile, pass `-tags linuxacl` to `go build`.

## Build without zstd (optional)

Where cgo is not available (for example when cross-compiling `dserver` for
another architecture), build without zstd. That binary can not read `.zst`
files.

```console
% DTAIL_NO_ZSTD=yes make build
```

Without the Makefile, pass `-tags nozstd` to `go build`.

## Proprietary features (optional)

`DTAIL_USE_PROPRIETARY=yes make build` (build tag `proprietary`) needs the
proprietary log format source, which is not in this repository; without it the
build fails. Leave it off for the regular open-source build.

# Install it

Automate these steps with your configuration management (Puppet, Chef,
Ansible, ...). The commands copy the example files from the clone's
[`examples`](../examples) directory.

1. Install `dserver` on every server, and `dtailhealth` for the
   [monitoring check](#monitor-it):

```console
% sudo install -o root -g root -m 0755 dserver dtailhealth /usr/local/bin/
```

2. Create the `dserver` run user and group (`dserver` refuses to run as root):

```console
% sudo adduser dserver
```

3. Create the directories. `dserver` creates its log directory itself, but not
   the cache directory, which holds its SSH host key and the key cache:

```console
% sudo mkdir -p /etc/dserver /var/run/dserver/cache
% sudo chown -R dserver:dserver /var/run/dserver
```

4. Install the [example config](../examples/dtail.json.example) as
   `/etc/dserver/dtail.json`:

```console
% sudo install -m 0644 examples/dtail.json.example /etc/dserver/dtail.json
```

5. Install the [example unit](../examples/dserver.service.example):

```console
% sudo install -m 0644 examples/dserver.service.example /etc/systemd/system/dserver.service
% sudo systemctl daemon-reload
```

The unit runs `/usr/local/bin/dserver -cfg /etc/dserver/dtail.json` as user
`dserver` in the working directory `/var/run/dserver`. The relative `LogDir`
(`log`), `CacheDir` (`cache`) and `HostKeyPath` (`cache/ssh_host_key`) of the
example config are resolved there.

## Persistent locations

`/var/run` (usually `/run`) is a `tmpfs`, so everything below
`/var/run/dserver` is gone after a reboot: the directory itself (the unit's
`WorkingDirectory`), the SSH host key, the key cache, the logs and the helper
scripts installed further down.

* Without the working directory the service does not start, so create the
  directories at boot (for example with `systemd-tmpfiles`) or use a
  persistent directory such as `/var/lib/dserver`.
* A host key that is generated anew makes clients refuse the server (`HOST KEY
  CHANGED`). Keep it on persistent storage with an absolute `Server.HostKeyPath`.
* If you move the directory or the scripts, change `WorkingDirectory=` and the
  `ExecStart=` paths in the units and the paths inside the scripts to match.

# Configure it

Review these settings in `/etc/dserver/dtail.json` before the first start. All
keys, defaults and the command-line flags that override them are described in
[server-configuration.md](server-configuration.md); the schema is
[`examples/dtail.schema.json`](../examples/dtail.schema.json).

| Key | Example config | What to decide |
| --- | --- | --- |
| `Server.SSHBindAddress` | `0.0.0.0` | Listens on every local IPv4 address. Set one address of the host to listen only there (or start with `-bindAddress ADDR`). |
| `Common.SSHPort` | `2222` | The port clients connect to. |
| `Server.Permissions` | everything readable | Which files each user may read. The example lists sample users; replace them. |
| `Server.MaxConnections` | `50` | Concurrent connections (built-in default `10`). |
| `Server.Schedule`, `Server.Continuous` | one disabled job | MapReduce jobs dserver runs itself. |

`dserver` reads `/etc/dserver/dtail.json` only because the unit passes `-cfg`.
Without `-cfg`, every DTail binary reads `~/.config/dtail/dtail.conf` or, if
that does not exist, `~/.dtail.conf`.

Related: [auth-key fast reconnect](auth-key-fast-reconnect.md) (enabled by
default). Reading `journal:unit.service` targets needs `journalctl` on the
server's `PATH`.

# Start it

The unit is not enabled by the steps above. Start it, and enable it if it
should start at boot:

```console
% sudo systemctl start dserver
% sudo systemctl enable dserver
% sudo systemctl status dserver
● dserver.service - DTail server
   Loaded: loaded (/etc/systemd/system/dserver.service; enabled; ...)
   Active: active (running) ...
     └─12296 /usr/local/bin/dserver -cfg /etc/dserver/dtail.json

    ... dserver[12296]: DTail 5.0.0 Protocol 5.0 Have a lot of fun!
```

With the example config only this version line goes to the journal. The log
lines (`Starting server`, `Binding server`, ...) go to the daily file
`/var/run/dserver/log/YYYYMMDD.log`.

### Firewall (firewalld on RHEL, Rocky Linux, Fedora, …)

Clients need TCP port `2222` (`Common.SSHPort`). A working ping does not mean
the port is open; a blocked port typically shows on the client as `dial tcp …:
connect: no route to host`. With `firewalld`:

```console
% sudo firewall-cmd --permanent --add-port=2222/tcp
% sudo firewall-cmd --reload
% sudo firewall-cmd --list-ports
```

[firewalld-dserver-port.sh.example](../examples/firewalld-dserver-port.sh.example)
does the same. For other firewalls (nftables, ufw, …) add an equivalent rule.

# Register SSH public keys in DTail server

The `dserver` user can not read `/home/USER/.ssh/authorized_keys`, so no user
could log in. `dserver` therefore also looks for a user's public keys in its
cache directory, `/var/run/dserver/cache/USER.authorized_keys`.

[update_key_cache.sh](../examples/update_key_cache.sh.example) copies the keys
of all users below `/home` there. Run it periodically, here with the example
timer (every 30 minutes), or from your configuration management:

```console
% sudo install -m 0755 examples/update_key_cache.sh.example /var/run/dserver/update_key_cache.sh
% sudo install -m 0644 examples/dserver-update-keycache.service.example /etc/systemd/system/dserver-update-keycache.service
% sudo install -m 0644 examples/dserver-update-keycache.timer.example /etc/systemd/system/dserver-update-keycache.timer
% sudo systemctl daemon-reload
% sudo systemctl start dserver-update-keycache.service
% sudo systemctl enable --now dserver-update-keycache.timer
```

The example units expect the script in `/var/run/dserver`; see
[Persistent locations](#persistent-locations).

# Prune old dserver log files

`dserver` writes one log file per day and does not delete old ones.
[prune_dserver_logs.sh](../examples/prune_dserver_logs.sh.example) deletes the
`*.log` files in `/var/run/dserver/log` that are older than seven days; the
example timer runs it daily:

```console
% sudo install -m 0755 examples/prune_dserver_logs.sh.example /var/run/dserver/prune_dserver_logs.sh
% sudo install -m 0644 examples/dserver-prune-logs.service.example /etc/systemd/system/dserver-prune-logs.service
% sudo install -m 0644 examples/dserver-prune-logs.timer.example /etc/systemd/system/dserver-prune-logs.timer
% sudo systemctl daemon-reload
% sudo systemctl enable --now dserver-prune-logs.timer
```

# Run DTail client

Use the clients as shown in the [Quick Starting Guide](quickstart.md) and the
[Examples](examples.md).

# Monitor it

Run the Nagios check [check_dserver.sh](../examples/check_dserver.sh.example)
locally on each server (e.g. via NRPE):

```console
% ./check_dserver.sh
OK: All fine at localhost:2222 :-)
```

The script runs `/usr/local/bin/dtailhealth --server localhost:2222`, which
exits `0` (OK), `1` (WARNING, e.g. without `--server`) or `2` (CRITICAL), as
Nagios expects. It trusts unknown host keys without asking; a server whose
host key changed is CRITICAL unless `--trustChangedHostKeys` is given.

# Upgrading

Clients and servers must both be DTail 5.x (protocol 5, see `--version`);
upgrade them together. What else changes for an existing installation is
listed in the [Upgrade Notes](upgrade-notes.md).
