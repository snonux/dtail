# Output, logging and exit statuses

## Output lines

- Remote payload lines are prefixed with their source:
  `REMOTE|host|...|file|...` followed by the line. Client diagnostics are
  `CLIENT|host|LEVEL|...`; periodic `STATS` lines report connected servers
  (`connected=2|servers=2|connected%=100|...`).
- `--plain` prints the raw payload only (implies `--quiet`, which only
  drops the periodic STATS lines, and `--noColor`). Colors are not turned
  off automatically when stdout is a pipe. Payload lines that look like
  protocol messages (`.syn close connection`, `AUTHKEY OK`) are printed as
  data, never acted on.
- dserver warnings (missing file, glob cap, long line split) reach the
  client whatever the server's log level; in server mode they appear even
  at a restrictive client log level, and with `--plain` a `WARN|...` line
  can land in redirected payload. Serverless warnings go through the
  client logger and its level.
- `dmap`/`dtail --query` print a result table (query shown above it,
  highlighted on a terminal; one plain run of text when piped so `grep`
  finds it). See [mapreduce-results.md](mapreduce-results.md).
- `--colorTable` / `--wideColorTable` (`dtail`) print the color palette.

## Client logging

- Default logger `fout`: payload to stdout, diagnostics to stdout **and** a
  daily file `~/log/YYYYMMDD.log` (`--logDir` or `Common.LogDir`).
  Payload is not copied into the file unless `--log-payload` /
  `Client.LogPayload: true`.
- `--logger stdout`: stdout only. `--logger none`: no logger.
  `--logger file`: file only (payload included, regardless of
  `--log-payload`).
- Precedence: flag > `Common.Logger`/`Common.LogDir` > default
  (`fout`, `~/log`).
- Every diagnostic detail escapes control characters (`\n`, `\x1b`,
  `\u202e`), so file names cannot forge log lines; payload is never
  escaped.

## Exit statuses

| Client | Status |
| --- | --- |
| `dcat`, `dgrep` | `0` success (also `dgrep` with no match); `1` connection refused/unreachable, SSH handshake or session failure, rejected `SESSION START`, missing `journal-v1`, other protocol version, serverless failure, session cut short (server killed mid-read, no close handshake), client could not start; `2` invalid flag syntax (rejected values such as `--regex '('`, a query that does not parse or `--discovery comma:a,b` exit `1`), or a server-reported failed command: `read: no file to read`, `read: no permission to read file`, `read: unable to read file`, `read: unable to create file reader`, `read: more files than the server reads`, `output: too large for the session output buffer`, `map: invalid query`, `command: ...`. Highest status across servers wins; partial output may precede a non-zero status. |
| `dtail` | Follow mode reconnects; its status does not report every failed read. |
| `dmap` | `0` even after `map: incomplete result` or a session cut short: it writes what it received and logs `The mapreduce result may be incomplete|<server>: <reason>` at WARN. `1` for a missing query without a terminal. Check WARN lines before trusting an outfile. |
| `dtailhealth` | `0` OK, `1` WARNING (also serverless), `2` CRITICAL. |

Scheduled and continuous jobs have their own failure rules; see
[dserver-jobs.md](dserver-jobs.md).

## Protocol compatibility

Clients and `dserver` speak protocol 5 and must be upgraded together;
nothing is negotiated.

- A protocol-5 client against an older dserver: `unsupported DTail server:
  not a protocol 5 server (no capabilities frame first); please update
  DTail server`, connection status `1`.
- An older (4.x or earlier) client against a protocol-5 dserver gets a
  version mismatch error and its session stays open until it exits or the
  server's idle timeout, so a one-shot `dcat` hangs instead of exiting. A
  4.x `dtailhealth` fails at once.
- Any other version (future, other 5.x minor) has its session ended
  without the close handshake.
- The only capability a server advertises is `journal-v1` (Linux with
  `journalctl`).
