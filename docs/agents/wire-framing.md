# Wire Framing (server-to-client stream)

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

Every server-to-client message (frame) ends with `protocol.MessageDelimiter`,
the single byte 0xAC. That byte is also a UTF-8 continuation byte (`€` is
E2 82 AC, `Ь` D0 AC, `¬` C2 AC, about 1/64 of all CJK characters), so raw
framing cut such lines in two and dropped the byte. Since protocol 5
(`protocol.ProtocolCompat` = `5.0`) every session uses
`protocol.EscapedControlFraming` for every frame, from its first frame (the
capabilities frame, held as the framer's first pending output,
`sessionFramer.queueFirstFrame`, so payload queued meanwhile can not overtake
it) on, in server and serverless mode and for health checks;
nothing is negotiated (`internal/protocol/framing.go`, `control_frames.go`;
design in `docs/dev/wire_framing.md`):
- Every frame body has 0xAC replaced by `FF 01` and 0xFF by `FF 02`, in time
  linear in the body however many bytes are escaped and with the escaped body
  sized exactly (counted first), so a dense 1 MiB body costs one allocation of
  its escaped size. Valid UTF-8 never contains 0xFF, so frames without 0xAC
  are written byte for byte.
- Every control frame (hidden `.syn ...` messages, the capabilities frame
  and the read progress (see "Read Progress") included, and `AUTHKEY`
  replies) starts with the marker `FF 03`, which
  escaping makes unforgeable; every unmarked frame is payload. So `--plain`
  log lines starting with `.` or reading `AUTHKEY OK` are printed and never
  acted upon (`.syn close connection`, `.syn command failed x`).
- The server (`sessionFraming` in `internal/handlers/session_framer.go`)
  never waits for a client request before it sends a frame; the client
  handlers (`internal/clients/handlers`) send no framing request. The former
  `.ack framing escaped-v1` / `control-v1` requests are unknown `.ack`
  commands now and ignored.
- `NewClientHandler` and `NewMaprHandler` check the session's first frame
  (`baseHandler.checkFirstFrame`): unless it is a marked capabilities frame
  (which may list no capability), they report `unsupported DTail server: not
  a protocol 5 server (no capabilities frame first); please update DTail
  server` and set status
  1, even after a normal close handshake (protocol-4.1 servers can finish a
  session on unknown `.ack` commands before reading files, which must never
  look like a successful empty query). The health handler does not check (the
  server's health handler sends no capabilities frame).
- The capabilities frame lists only runtime properties of a server, today
  just `journal-v1` (Linux with `journalctl`), so it may be empty
  (`handlers.ServerCapabilities`). What every protocol 5 peer supports is not
  advertised: the former `query-update-v1`, `command-failure-v1`,
  `escaped-framing-v1`, `encoded-read-path-v1` and `control-frames-v1` are
  gone, and so are the client checks of them.
- The server rejects command frames of another protocol version
  (`commandDispatcher.rejectProtocolVersion`, see "Client Exit Statuses").
  `protocol.TerminalFraming` (verbatim, unmarked) only lends the frame layout
  to serverless terminal output and is never sent to a client.
- Escaping can grow a frame to almost twice its raw size, so the output buffer
  cap of every client session (`ServerHandler`) is at least
  `2*MaxLineLength + 128 KiB` (see the output limits below); health sessions
  send only a few short frames and keep the default. Output that still does not fit is reported, never dropped
  silently: a visible warning plus the failed-command reason `output: too
  large for the session output buffer`.
