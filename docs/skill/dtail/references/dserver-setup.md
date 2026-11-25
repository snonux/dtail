# dserver setup and operation

`dserver` is the SSH server every target host runs (port 2222). It reads
files for the clients with the user's permissions and runs scheduled and
continuous MapReduce jobs.

## Build

```sh
make build                       # all binaries into the repo root
DTAIL_USE_ACL=yes make build     # Linux ACLs (needs libacl-devel); tag linuxacl
DTAIL_NO_ZSTD=yes make build     # CGO-free / cross builds, no .zst; tag nozstd
DTAIL_USE_PROPRIETARY=yes make build   # fails without the non-public log format sources
go install ./cmd/dserver ./cmd/dcat ./cmd/dgrep ./cmd/dmap ./cmd/dtail ./cmd/dtailhealth  # from a clone, into $GOBIN
```

The required Go version is in `go.mod`; the default build needs cgo and a C
compiler (zstd). **Never** `go install github.com/mimecast/dtail/cmd/...@latest`:
the module path has no major-version suffix, so `@latest` installs v1.1.0.
`make install` is broken too (it installs every binary as `main`). Make
sure the clone holds the protocol-5 sources (`dserver --version` prints
`Protocol 5.0`). `make build-pgo` / `make pgo` build
profile-optimized binaries into `pgo-build/` (see
[development.md](development.md)).

## Install (systemd, RHEL-like)

1. Binary to `/usr/local/bin/dserver` (root:root, 0755).
2. Run user and group `dserver` (dserver refuses to run as UID/GID 0);
   directories `/etc/dserver` and `/var/run/dserver` (owned by `dserver`;
   the unit's working directory).
3. Config: `examples/dtail.json.example` -> `/etc/dserver/dtail.json`
   (see [dserver-config.md](dserver-config.md)).
4. Unit: `examples/dserver.service.example` (runs
   `dserver -cfg /etc/dserver/dtail.json`), then `systemctl daemon-reload`,
   `enable --now dserver`.
5. Firewall: open `2222/tcp` (`examples/firewalld-dserver-port.sh.example`).
   `connect: no route to host` from clients often means a firewall reject.
6. User keys, looked up in this order: `Server.AuthorizedKeysPath` (one
   file for all users), else `<CacheDir>/USER.authorized_keys`
   (`/var/run/dserver/cache/...` under the unit), else
   `~USER/.ssh/authorized_keys`, which the `dserver` user usually cannot
   read. Fill the cache every 30 min with
   `examples/update_key_cache.sh.example` and the
   `dserver-update-keycache.{service,timer}.example` units.
7. Log pruning: `examples/prune_dserver_logs.sh.example` with the
   `dserver-prune-logs.{service,timer}.example` units.
8. Monitoring: `examples/check_dserver.sh.example` (Nagios/NRPE, run on
   the host; prints `OK: All fine at localhost:2222 :-)`), or
   `dtailhealth --server HOST:2222` from anywhere (`OK`/`WARNING`/
   `CRITICAL`, exit 0/1/2; a refused connection is CRITICAL).

On first start dserver generates an RSA host key (`Server.HostKeyPath`,
default `./cache/ssh_host_key`, `Server.HostKeyBits` 4096).

## dserver flags

| Flag | Meaning |
| --- | --- |
| `-cfg PATH` | Config file (`none` = no file; default search as for clients: `~/.config/dtail/dtail.conf`, then `~/.dtail.conf`). |
| `-port N` | SSH port (default 2222, or `Common.SSHPort`). |
| `-bindAddress ADDR` | Listen address (default `Server.SSHBindAddress`, `0.0.0.0`). |
| `-host-key-path`, `-authorized-keys-path` | Host key and a single authorized_keys file. |
| `-logger`, `-logDir`, `-logLevel` | Logging (default logger `file`, dir `log`, level `info`; flags beat `Common.*`). |
| `-hostname-override` | Hostname in logs and output. |
| `-shutdownAfter N` | Exit after N seconds (tests). |
| `-color`, `-pprof ADDR`, `-version` | Colors, pprof server, version (prints the protocol). |

Quick manual run: `./dserver --logger stdout --logLevel debug --port 2222`.
Under systemd with the default `file` logger only the version line reaches
the journal; everything else is in `LogDir/YYYYMMDD.log`.

## Operating notes

- dserver's log level only affects its own log; warnings to clients are
  always sent. Log lines escape control characters.
- The default logger `file` writes to `Common.LogDir` (`log`):
  `YYYYMMDD.log` with `Common.LogRotation: daily` (default), else
  `dserver.log`; SIGHUP reopens the file; dserver never deletes old logs.
  An old config with `"Logger": "Fout"` also tees to stdout (journal under
  systemd).
- Upgrade clients and all dservers together (protocol 5). During a rolling
  upgrade, jobs of a not-yet-upgraded 4.x dserver that read from upgraded
  ones hang until `IdleSessionTimeoutS`.
- After an upgrade, `grep 'Unable to create job' log/*.log` finds job
  queries the stricter parser now rejects; check them with
  `dmap --query '...'` first.
- `Server.MaxConnections` (10) bounds connections including handshakes;
  one connection carries one session.
