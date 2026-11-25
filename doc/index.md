DTail Documentation
===================

## Installation and Usage

| Document | Content |
|----------|---------|
| [Quick Starting Guide](./quickstart.md) | Install, start a `dserver`, run the first commands |
| [Usage Examples](./examples.md) | `dtail`, `dcat`, `dgrep`, `dmap`, serverless mode, read progress, exit statuses |
| [Client Connections](./client-connections.md) | Config file, SSH keys, host key verification, timeouts, client logs |
| [Exit Statuses](./exit-statuses.md) | What the exit statuses of the clients mean |
| [Read Progress](./read-progress.md) | `--progress` and the progress fields of `dcat`, `dgrep` and `dmap` |
| [Installation Guide](./installation.md) | Build options, `systemd` service, key cache, monitoring |
| [Server Configuration Guide](./server-configuration.md) | Read permissions, limits, journal reads, shared reads, scheduled jobs |
| [Read Permissions](./read-permissions.md) | How the `readfiles` rules are evaluated |
| [Scheduled Jobs](./scheduled-jobs.md) | Scheduled and continuous MapReduce jobs, failures and retries |
| [Upgrade Notes](./upgrade-notes.md) | Behaviour changes to know before upgrading |

## Advanced topics

| Document | Content |
|----------|---------|
| [DTail Query Language](./querylanguage.md) | The SQL-like MapReduce language |
| [Log Formats](./logformats.md) | Log formats for MapReduce queries, writing your own |
| [Interactive Mode](./interactive-query.md) | Changing a running session, Tab completion, the `dmap` query builder |
| [Control Channel for Programs and Agents](./agent-control.md) | `--control json`: the interactive features as JSON requests and responses |
| [Interactive Mode Reference](./interactive-query-reference.md) | Every command, flag, key and session rule of the interactive mode |
| [Auth-Key Fast-Reconnect](./auth-key-fast-reconnect.md) | Faster authentication on repeated connections |
| [Testing Guide](./testing.md) | Unit and integration tests |
| [Licenses](./licenses.md) | Licenses of DTail and its dependencies |
