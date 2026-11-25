DTail Documentation
===================

## Installation and Usage

* To get a taste, please have a look at the [Usage Examples](./examples.md).
* The [exit statuses of `dcat` and `dgrep`](./examples.md#exit-statuses-of-dcat-and-dgrep) tell missing, denied or unreadable files (`2`) from servers that could not be connected to, failed the SSH handshake or whose session was cut short mid-read, e.g. a killed `dserver` (`1`).
* Follow the [Quick Starting Guide](./quickstart.md) if you are looking into DTail your first time. It also covers SSH keys and the SSH agent, host key verification (`known_hosts`, `--trustAllHosts`, `--trustChangedHostKeys`), the config file locations and the connect and handshake timeouts (`Common.SSHConnectTimeoutMs`, `Common.SSHHandshakeTimeoutMs`).
* For a more sustainable setup, please follow the [Installation Guide](./installation.md).
* To configure `dserver`, see the [Server Configuration Guide](./server-configuration.md) — read permissions, connection limits, journal reads, shared reads and scheduled jobs.

## Advanced topics

* The [DTail Query Language](./querylanguage.md) is the starting point to dig deeper into DTail's own SQL-like mapreduce language for extraction/aggregation stats from log files.
* The [Interactive Query Reload](./interactive-query.md) documentation describes the interactive mode `dtail` starts in by default on a terminal (`--no-interactive` turns it off, `--interactive-query` turns it on for the other clients), the `dmap` query builder that opens when `dmap` is started without a query on a terminal, `:reload`, `:query` and `:tail` (switching a running `dtail` between following lines and a MapReduce query), `:show`, `:fields`, `:probe`, `:help`, `:quit`, the reload flags per mode, line editing with guided Tab completion of commands, flags, queries (tables and fields the servers' logs carry) and remote file paths, editing the line in `$EDITOR` (Alt-E), the optional vi mode (`--edit-mode vi`), pausing the output, colors, and session reuse semantics.
* [Log Formats](./logformats.md) explains how to create your own custom log format for use with mapreduce queries.
* The [Auth-Key Fast-Reconnect](./auth-key-fast-reconnect.md) documentation explains how clients register a temporary public key with `dserver` to skip the full SSH authentication on reconnects.
* The [client log file](./examples.md#how-to-use-dcat) keeps diagnostics only; `--log-payload` also tees the retrieved payload into it.
* The [serverless mode](./examples.md#how-to-use-the-dtail-serverless-mode) (see the Usage Examples) works without any `dserver` and without SSH networking.
* Check out the [Testing Guide](./testing.md) for unit and integration testing.
