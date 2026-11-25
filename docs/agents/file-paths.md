# File Paths With Spaces

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

dserver splits every command on single spaces and a read command is
`<mode>:<options> <path> <regex>`, so a path is one word. `session.Spec.Commands`
(the one builder of read commands, for the plain command stream of
non-interactive clients and for the commands dserver builds from
`SESSION START`/`UPDATE` Files, including the `timeout N <mode> <path>
<regex>` form) passes every path through
`protocol.EncodeReadPath` (`internal/protocol/readpath.go`): a path containing a
space, or starting with the prefix `base64%`, is sent as `base64%` + standard
base64 of the path (the prefix config option values already use); every other
path is sent verbatim. `readCommand.parsePath`
decodes the word before anything else sees it, so globbing, the `readfiles`
permission check (which must match the decoded path), `journal:` targets and
shared reads all use the real path; a prefixed word that does not decode fails
the command with `read: unable to parse command` and reads nothing.

**Compatibility:** every protocol 5 dserver (and the serverless runtime)
decodes encoded paths, so there is no capability for it and clients send such
paths to every server (the former `encoded-read-path-v1` capability is gone).
Via `--interactive-query` (`SESSION START`/`UPDATE`, Files as JSON) dserver
builds and encodes the commands itself.

**Commas:** `--files`, positional arguments (joined with commas) and job `Files`
are split at commas with blanks around each entry trimmed (`splitFiles` in
`internal/session/spec.go`), so paths containing a comma, or with leading or
trailing blanks, cannot be read. The scheduler's job grouping parses the same
comma lists, so this is not changed.

**Newlines:** the read permission check denies every path holding a newline
(`errNewlineInPath` in `sessionuser.User.iteratePaths`, the resolved path, so
also through a symlink; `PathRules.MayReadBeneath` offers no such directory):
`.` matches no newline and `$` only the end, so `!^/dir/.*$` did not match
`/dir/a\nb` while an allow `^/dir/` did, a bypass of the documented deny
form (`TestResolveReadTargetDeniesNewlinePaths`). The read reports `read: no
permission to read file`. dserver's own jobs are not checked and read them.
