# Diagnostic Escaping (dserver and client logs)

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

Every diagnostic detail (the args of `Info`/`Warn`/`Error`/..., strings,
errors and `%v` values alike) is written through
`protocol.writeEscapedDetail` (`internal/protocol/diagnostics_escape.go`,
called by `writeDiagnosticDetails`): C0 controls (tab included), DEL, C1
controls, Cf format characters (e.g. U+202E) and U+2028/U+2029 become Go
escapes (`\n`, `\r`, `\t`, `\xNN`, `\uNNNN`), so a path, glob or probe entry
with a newline can not forge log lines and an ESC can not drive the terminal.
Printable text, non-ASCII text, invalid UTF-8 bytes (e.g. Latin-1 names)
and backslashes are kept byte for byte (normal paths are unchanged; escaping
is idempotent), except a lone invalid byte 0x80-0x9f, which an 8-bit
terminal takes for a C1 control and which becomes `\xNN`. Header
fields (source, hostname, level, timestamp) are not escaped. Stack traces
logged as a detail are one line with `\n\t` escapes. Payload (`Raw`) is never
escaped, and nothing the client parses changes: control frames (`.syn ...`,
`AUTHKEY`) are built without the diagnostic writer, and the server warnings a
client receives are payload frames it only prints.
