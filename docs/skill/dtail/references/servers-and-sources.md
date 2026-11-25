# Servers and sources

## Select servers

- `--servers host1,host2:2223` connects to several `dserver`s; the default
  port is `2222` (`--port` changes the default, an explicit `HOST:PORT`
  wins).
- `--servers servers.txt` (a path to an existing file) reads one `HOST` or
  `HOST:PORT` per line; blank lines and `#` comments are ignored, blanks
  around entries trimmed. An empty server list is an error.
- `--discovery COMMA` forces a comma list, `--discovery FILE` a list file.
  These two take no `:options` suffix (`comma:a,b` is an error). Builds may
  add discovery modules; a `/regex/` server filter only applies to those.
- `--cpc N` (default 10) bounds connection attempts per CPU core; DTail
  scales to thousands of servers per client.
- `--user NAME` is the SSH user (default: the local user).

## Serverless mode

No `--servers` (and no discovery module), or `--servers serverless`, runs
the client in-process on the local machine: no SSH, no `dserver`. All four
data clients support it, including `dmap` and `dtail --query`.

- Without `--files`, serverless mode reads standard input when stdin is a
  pipe: `printf 'a\n' | dgrep --regex a`.
- A whitespace-only `--servers` value is an error, not serverless mode.
- `dcat --plain` in serverless mode reproduces a regular file byte for byte,
  except that a missing final newline is added.
- Local read permissions are the OS ones; `Server.Permissions` from a
  `--cfg` file apply too.
- `dtailhealth` without `--server` tests serverless mode and reports
  `WARNING`.

## Select files

- `--files` takes a comma-separated list of files, paths or globs;
  positional arguments are joined into the same list. Blanks around each
  entry are stripped.
- Paths containing a comma, or leading/trailing blanks, cannot be addressed.
  Paths with interior spaces work (quote them); every protocol-5 `dserver`
  decodes them.
- Globs expand on the server (`filepath.Glob`), capped at
  `Server.MaxGlobTargets` (default 1000; the client is told `read: more
  files than the server reads`). A glob that matches nothing is retried for
  a while and then reported as `read: no file to read`. Directories and
  other non-regular matches are skipped silently.
- `.gz`, `.gzip` and `.zst` are decompressed by suffix. Builds made with
  `DTAIL_NO_ZSTD=yes` cannot read `.zst`.
- Lines longer than `Server.MaxLineLength` (default 1 MiB) are split into
  pieces with a `Long log line, splitting into multiple lines` warning.

## Journal targets

`journal:UNIT.TYPE` reads the systemd journal of one unit instead of a file,
e.g. `--files 'journal:nginx.service'`.

- `dcat`, `dgrep`, `dmap` read a snapshot once; `dtail` follows new entries
  (`journalctl -f -n 0`, restarted until cancelled).
- Linux only, `journalctl` on `PATH`. A remote server must advertise the
  `journal-v1` capability; the client refuses journal targets otherwise.
- The unit must be one literal unit name with an explicit type
  (`.service`, `.socket`, `.timer`, `.scope`, `.mount`, `.automount`,
  `.swap`, `.target`, `.path`, `.device`). Refused: globs
  (`journal:ssh*.service`), no type (`journal:sshd`), slices, templates
  (`journal:getty@.service`). Instances such as `getty@tty1.service` work.
- The server's `readfiles` rules must match the full target string,
  e.g. `^journal:.*\.service$`.

## Remote access rules

Every remote read is checked against the user's `Server.Permissions`
(`readfiles` regex rules, deny wins) and the OS file permissions; see
[dserver-config.md](dserver-config.md#read-permissions). A path holding a
newline is never readable. A missing, denied or unreadable file makes
`dcat`/`dgrep` exit `2` (see [output-and-status.md](output-and-status.md)).
