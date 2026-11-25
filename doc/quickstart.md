Quick Starting Guide
====================

Install DTail, start a server and run the first commands. For a permanent
setup (``systemd`` service, key cache, monitoring) follow the
[Installation Guide](installation.md).

You need Go, a C compiler (the zstd support uses cgo; the
[Installation Guide](installation.md) describes a build without it) and an SSH
key pair.

# Install it

```console
% git clone https://github.com/mimecast/dtail.git
% cd dtail
% make install
```

``make install`` installs these binaries into ``$GOBIN`` (default ``$HOME/go/bin``):

| Binary | Purpose |
|--------|---------|
| ``dserver`` | The DTail server |
| ``dtail`` | Follow log files (distributed tail) |
| ``dcat`` | Display whole files (distributed cat) |
| ``dgrep`` | Search files with a regex (distributed grep) |
| ``dmap`` | Run distributed MapReduce queries |
| ``dtailhealth`` | Health check of a ``dserver`` |

* ``make build`` compiles them into the repository root instead, plus the developer helper ``dtail-tools``.
* Without ``make``: ``go install ./cmd/dcat ./cmd/dgrep ./cmd/dmap ./cmd/dtail ./cmd/dserver ./cmd/dtailhealth``.
* Do not use ``go install github.com/mimecast/dtail/cmd/...@latest``: ``@latest`` resolves to the old v1.1.0.
* Clients and servers must both be DTail 5.x (protocol 5). Check with ``--version``.

# Start DTail server

Copy ``dserver`` to every server machine (e.g. ``serv-001.lan.example.org``) and start it there:

```console
% mkdir -p cache
% ./dserver --cfg none --logger stdout --port 2222
DTail 5.0.0 Protocol 5.0 Have a lot of fun!
INFO|1125-181549|Starting server|DTail 5.0.0 Protocol 5.0 Have a lot of fun!
INFO|1125-181549|Generating private server RSA host key
INFO|1125-181550|Starting server
INFO|1125-181550|Binding server|0.0.0.0:2222
INFO|1125-181550|Starting scheduled job runner after 2s
INFO|1125-181550|Starting continuous job runner after 2s
```

* ``dserver`` listens on TCP port 2222 on all addresses (``--bindAddress`` restricts it).
* A user may connect with every SSH key in their ``~/.ssh/authorized_keys`` on that machine.
* The host key is generated on the first start and stored in ``./cache/ssh_host_key`` (``HostKeyPath``). The ``cache`` directory must exist: otherwise the key is not saved, and the clients see a changed host key after every restart.
* Without ``--logger stdout`` the server logs to files in ``./log``.
* ``dserver`` refuses to run as root.
* By default every authorized user may read every file the ``dserver`` process can read. Restrict that with ``Server.Permissions``.

Configuration: [Server Configuration Guide](server-configuration.md).

# Setup DTail client

## Setup SSH

Put your public SSH key into ``~/.ssh/authorized_keys`` on every server machine. The private key stays on your workstation.

The client uses ``~/.ssh/id_rsa`` (or the file given with ``--auth-key-path``) and the keys of a running SSH agent:

```console
% ssh-add ~/.ssh/id_ed25519
```

If the machines also run OpenSSH, ``ssh serv-001.lan.example.org`` without a password prompt shows that the key works. DTail itself does not need OpenSSH.

Other key files, the key order, ``--user`` and password-protected keys: [client-connections.md](client-connections.md#ssh-keys).

## Run DTail client

```console
% dtail --servers serv-001.lan.example.org,serv-002.lan.example.org --files "/var/log/service/*.log"
Encountered 2 unknown hosts: 'serv-001.lan.example.org:2222,serv-002.lan.example.org:2222'
Do you want to trust these hosts?? (y=yes,a=all,n=no,d=details): y
CLIENT|workstation01|INFO|Added hosts to known hosts file|/home/user/.ssh/known_hosts
REMOTE|serv-001|100|1|service.log|2025-09-23T22:25:01|INFO|Service started
REMOTE|serv-002|100|1|service.log|2025-09-23T22:25:01|INFO|Service started
CLIENT|workstation01|INFO|STATS:STATS|connected=2|servers=2|connected%=100|new=2|throttle=0|goroutines=37|cgocalls=7|cpu=8
```

* Unknown hosts are asked about once and stored in ``~/.ssh/known_hosts``. ``--trustAllHosts`` skips the question.
* A host whose key changed is refused with ``HOST KEY CHANGED``.
* The ``STATS`` lines report the connected servers.
* On a terminal ``dtail`` is interactive: type ``:help``, leave with ``:quit``. ``--no-interactive`` turns that off.

The other clients take the same ``--servers`` and ``--files``:

```console
% dcat --servers serv-001.lan.example.org --files /etc/hostname
% dgrep --servers serv-001.lan.example.org --regex ERROR --files "/var/log/service/*.log"
% dmap --servers serv-001.lan.example.org --files "/var/log/service/*.log" \
    --query 'from STATS select count($line) group by $hostname'
```

Their ``STATS`` lines also show how far the files have been read; ``--progress`` writes that as ``PROGRESS`` lines to stderr. More commands: [examples.md](examples.md).

Host key verification, the config file (``~/.config/dtail/dtail.conf``), timeouts and the client log file (``~/log/YYYYMMDD.log``): [client-connections.md](client-connections.md).

# What else can it do?

| Feature | In short | Details |
|---------|----------|---------|
| Serverless mode | Omit ``--servers`` (or pass ``--servers serverless``) to read local files without any ``dserver`` | [examples.md](examples.md) |
| Journal reads | On Linux servers with ``journalctl``, the file target ``journal:unit.service`` reads that unit's systemd journal | [server-configuration.md](server-configuration.md) |
| Auth-key fast reconnect | On by default: the client registers a key with ``dserver``, so later connections authenticate faster | [auth-key-fast-reconnect.md](auth-key-fast-reconnect.md) |
| Interactive mode | Change the filter, files or query of a running session (``:reload``, ``:query``, ``:tail``), with Tab completion. Default for ``dtail`` on a terminal, ``--interactive-query`` for the other clients | [interactive-query.md](interactive-query.md), [reference](interactive-query-reference.md) |
| Query builder | ``dmap`` without a query on a terminal probes the files and guides through the query with Tab | [interactive-query.md](interactive-query.md) |
| MapReduce queries | SQL-like aggregation of log lines | [querylanguage.md](querylanguage.md), [logformats.md](logformats.md) |

An interactive ``dtail`` switching between a filter and a query, and the ``dmap`` query builder:

![dtail-interactive](dtail-interactive.gif "Interactive dtail")

![dmap-query-builder](dmap-query-builder.gif "The dmap query builder")
