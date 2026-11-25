# Journal Source Reads

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

Journal targets use `journal:unit.service` syntax. Permission rules should match the full target, for example `Server.Permissions.Users[user]: ["readfiles:^journal:.*\\.service$"]`.

**Technical Details:**
- Server capability `journal-v1` is required for any journal-backed read target; clients reject journal sources when the server does not advertise it.
- The capability is advertised only on Linux when `journalctl` is available on `PATH`.
- Journal reads use `journalctl` via `os/exec` only; there is no cgo or `libsystemd` path, and the feature is Linux-only at runtime.
- Non-follow reads the current journal snapshot once. Follow mode adds `-f -n 0` and keeps restarting `journalctl` until canceled.
- Entries are cut at `Server.MaxLineLength` exactly like file lines (shared `fs.SplitLine`/`fs.ScannerTokenLimit`, same long-line warning, passed via `journal.ReaderOptions.MaxLineLength`), so an entry over 1 MiB no longer fails with `bufio.Scanner: token too long`. An unterminated last entry of a `journalctl` run is still dropped (pieces cut off before it are kept).
- The unit must be one literal systemd unit name (`validateJournalSpec`,
  `internal/io/fs/journalunit.go`, used by reads, probes and the unit
  listing through `fs.NewValidatedJournalTarget`), as the `readfiles` check
  matches the target string while `journalctl -u` would widen other names:
  ASCII letters, digits and `:-_.\@` only (no glob `*?[`, which `journalctl
  -u` expands over the journal's units, so `journal:ssh*.service` read
  `sshd.service` past a deny rule), at most 255 bytes, no leading `-`, a
  non-empty name before `@` and the type, a non-empty instance (templates
  such as `getty@.service` are refused), and an explicit unit type suffix
  (`.service`, `.socket`, `.timer`, `.scope`, `.mount`, `.automount`,
  `.swap`, `.target`, `.path`, `.device`): `journal:sshd` is refused, as
  `journalctl` appends `.service` to it, and so is `.slice`, as a slice
  reads all units in it. A refused target fails like an unreadable file
  (`read: unable to read file`) after the permission check, and journalctl
  never runs.
