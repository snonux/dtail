DTail Read Permissions
======================

The details of `Server.Permissions`, which decides what each SSH user may
read from a `dserver`. The short version is in the
[Server Configuration Guide](./server-configuration.md).

## The rule list

```json
"Permissions": {
  "Default": [
    "readfiles:^/var/log/.*$"
  ],
  "Users": {
    "jamesblake": [
      "readfiles:!^/var/log/secret/.*$",
      "readfiles:^/var/log/.*$"
    ]
  }
}
```

| Key | Default | Meaning |
| --- | --- | --- |
| `Permissions.Default` | `["^/.*"]` | Rules of every user without an own entry. |
| `Permissions.Users` | *(none)* | User name to rule list. A user's list replaces `Default`, it is not added to it. |

`Default` keeps `["^/.*"]` (every file the OS lets the user read) when the
file has no `Permissions`, or a `Permissions` object without a `Default` key.
So always set `Default` explicitly.

A user whose rule list is empty (`"Default": []`, or an empty own list) gets
no session at all: `dserver` rejects it with `empty set of permission, user
won't be able to open any files`.
An empty `Default` therefore also locks out `dserver`'s own jobs and
`dtailhealth`, whose users have no own entry.

## How a rule is evaluated

A rule is `[readfiles:][!]<regex>`.

| Part | Meaning |
| --- | --- |
| `readfiles:` | Permission type. Optional, a bare regex means the same. |
| `!` | Deny rule. Without it the rule allows. |
| `<regex>` | Go `regexp` syntax, unanchored unless it uses `^`/`$`. |

* **Deny wins.** The list is scanned in order; the first matching deny rule
  rejects the path, whatever allow rules stand before or after it.
* **Deny by default.** A path no allow rule matches is denied.
* A rule whose regex does not compile fails every check that reaches it.
* The OS permissions are checked as well: the user needs OS read access to
  the file (or the `dserver` run user does, in a build without Linux ACL
  support).

## What the regex is matched against

The resolved absolute path: symlinks are resolved first. Write rules for the
real path, not for a symlink.

A rule matches the path string, not the file's identity. On a file system
that ignores case or Unicode normalization (ext4 with casefold, vfat, CIFS,
macOS), `/var/log/SECRET/x` is the same file as `/var/log/secret/x` but
passes the deny rule `!^/var/log/secret/`. Write case-insensitive rules
there: `!(?i)^/var/log/secret/`.

## Paths with a newline

A path holding a newline, in the file name or in a directory name above it
(after symlinks are resolved), is never readable, whatever the rules say.
The read fails with `read: no permission to read file`, and the path listing
offers neither the path nor a directory with such a name.

The reason: in a rule `.` matches no newline and `$` only the end of the
path. The deny rule `!^/var/log/secret/.*$` would not match
`/var/log/secret/a<newline>b`, while the allow rule `^/var/log/` would.
Whoever could create such a name beneath a denied directory could have it
read past the deny rule. Rules therefore never need to think of newlines.

## Journal targets

A journal target is a string like `journal:nginx.service`, and a rule must
match that whole string, `journal:` prefix included:

```json
"readfiles:^journal:.*\\.service$"
```

### The unit must be one literal unit name

The rule that matched the target must cover exactly what `journalctl -u`
reads. So the unit is restricted to:

* ASCII letters, digits and `:-_.\@`, at most 255 bytes, no leading `-`,
* an explicit type suffix: `.service`, `.socket`, `.timer`, `.scope`,
  `.mount`, `.automount`, `.swap`, `.target`, `.path` or `.device`,
* for an instance such as `getty@tty1.service`, a non-empty instance.

| Refused target | Why |
| --- | --- |
| `journal:ssh*.service` | `journalctl` would expand the glob over all units. |
| `journal:sshd` | `journalctl` would read it as `sshd.service`. |
| `journal:system.slice` | A slice reads every unit in it. |
| `journal:getty@.service` | A template, not a unit. |

A refused target is reported as `read: unable to read file`, after the
permission check. A deny rule such as `"readfiles:!^journal:sshd\\.service$"`
can therefore not be bypassed by spelling the target differently.

### What a deny rule on a unit does not cover

* **Messages about the unit logged by other units.** They stay readable
  through those units: PID 1's messages in `journal:init.scope`, coredump
  reports under the `systemd-coredump@...service` unit, a user's units under
  `user@UID.service`. Deny those too where that matters.
* **Aliases.** `journalctl -u` does not resolve them, so name the canonical
  unit. On Debian and Ubuntu `sshd.service` is an alias of `ssh.service`: a
  deny rule on `sshd\\.service` protects nothing there.

## Jobs bypass the rules

`dserver`'s own scheduled and continuous jobs run as the internal users
`DTAIL-SCHEDULE` and `DTAIL-CONTINUOUS`. The rules do not apply to them: they
read what their job configuration names, also paths with a newline. See
[Scheduled and continuous jobs](./scheduled-jobs.md).

## The path listing of the interactive completion

The clients' interactive path completion asks `dserver` for a listing of a
directory. The same rules bound it:

* Files: only those the user may read.
* `journal:` units: only those the user may read.
* Directories: offered when an allow rule can match some path beneath them,
  unless a deny rule already matches the directory path itself.

Whether a deny rule hides a directory name:

| Deny rule | Hides `secret` in `/var/log`? |
| --- | --- |
| `!^/var/log/secret/` | Yes. |
| `!^/var/log/secret/.*$` | Yes: it matches `secret/` and everything beneath. |
| `!^/var/log/secret/.+$` | No: `.+` needs a character after the `/`. |
| `!^/var/log/secret/.*\.log$` | No: it only matches files beneath. |
| `!^/var/log/[^/]*$` | No: it covers only the files right in `/var/log`; subdirectories stay reachable. |

So to hide a directory, write a deny rule for the directory path itself. The
read check is not affected: files a deny rule matches are denied either way,
only the directory's name may show.

Further notes:

* An unanchored allow rule such as `\.log$` makes every directory listable
  that the OS lets `dserver` read.
* A directory the user may not list looks the same as one that does not
  exist.
* A `..` in the typed path removes the directory before it without looking
  at it (as `filepath.Clean` does), so it does not tell whether that
  directory exists.
* `Server.PathCompletionDisable: true` turns the listing off; a request is
  then answered `probe: path listing disabled`.

The exact matching, bounds and logging of the listing are described in the
developer note [docs/dev/probe.md](../docs/dev/probe.md).
