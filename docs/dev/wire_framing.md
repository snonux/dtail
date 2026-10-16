# Wire framing of the server-to-client stream

The server-to-client byte stream of a DTail session (over SSH, or in-process
in serverless mode) is a sequence of frames. Each frame ends with
`protocol.MessageDelimiter`, the single byte `0xAC`. A frame body is one of:

* `REMOTE|host|percent|line number|source id|content` — a log line
  (`protocol.Framing.EncodeLine`),
* `SERVER|host|message` and `AGGREGATE|host|data` — server messages and
  MapReduce aggregates (`protocol.Framing.EncodeMessage`),
* an untagged payload: a plain-mode log line, a hidden control message
  starting with `.` (e.g. `.syn capabilities ...`, `.syn close connection`).

The client-to-server direction is unchanged: `protocol 4.1 base64 <command>;`.

## The problem with 0xAC

`0xAC` is `'¬'` truncated to a byte. It is also a UTF-8 continuation byte and
occurs in every character whose code point is `0x2C` modulo 64: `¬` (C2 AC),
`€` (E2 82 AC), `Ь` (D0 AC), `Ĭ`, `Ŭ` and about 1/64 of all CJK characters
(e.g. `丬`, E4 B8 AC). With the legacy raw framing a client cut a log line,
group key or aggregate value containing such a character at that byte, lost
the byte, and printed two broken lines (or failed to parse an aggregate).

## Escaped framing

`protocol.EscapedFraming` escapes two bytes in every frame body, header
fields included:

| body byte          | on the wire |
|--------------------|-------------|
| `0xAC` (delimiter) | `FF 01`     |
| `0xFF` (escape)    | `FF 02`     |

All other bytes are written unchanged. `0xFF` never occurs in valid UTF-8,
so a UTF-8 frame without `0xAC` (in particular every ASCII frame) is
byte-identical in both framings, and the escaped stream contains `0xAC` only
as the delimiter. Decoding (`Framing.DecodeFrame`) returns frames without
`0xFF` as they are, without copying; malformed sequences are kept verbatim.

## Negotiation and mixed versions

The framing is chosen per session, so every client and server version keeps
working with every other one:

1. The server queues its capabilities frame (`.syn capabilities ...`) when the
   session handler is built. It is the first frame of the session and always
   raw. dserver and the in-process serverless server list
   `escaped-framing-v1` (`protocol.CapabilityEscapedFramingV1`).
2. The client handlers for dcat, dgrep, dtail and dmap (`NewClientHandler`,
   `NewMaprHandler`) send `.ack framing escaped-v1`
   (`protocol.EscapedFramingCommand`) as their very first command frame,
   before `AUTHKEY` and `.ack input begin`, whichever connector drives them.
   They do not wait for the capabilities, so the request costs no round trip.
3. A server that advertised the capability switches the session to escaped
   framing when it handles the request (`ServerHandler.handleAckControl`). The
   switch waits until the capabilities frame has been encoded
   (`sessionFramer.enableEscapedFraming`; in practice it never waits) and runs
   on the command input goroutine, so every frame of a later command is
   escaped, including line frames that a read command pre-encodes
   (`NewFramedNetworkWriter`).
4. The client decodes every frame after a capabilities frame listing
   `escaped-framing-v1` as escaped, if it sent the request.

| client \ server              | older dserver           | current dserver |
|------------------------------|-------------------------|-----------------|
| older client (no request)    | raw                     | raw             |
| current client (request)     | raw (`.ack` ignored)    | escaped         |

Older dservers of this fork ignore the unknown `.ack framing escaped-v1` like
every `.ack` they do not know (the same property `.ack input begin` relies
on), and do not advertise the capability, so the client stays on raw framing.
Older clients never send the request. Both raw combinations keep the legacy
behaviour, including the corruption of lines containing `0xAC`;
`ProtocolCompat` stays `4.1`, so no combination is rejected.

Released v4.3.x dservers, which predate this fork's command batches, end a
session as soon as a command completes while no other one runs, so they
already ended the session on a fork client's first `AUTHKEY` or
`.ack input begin` before escaped framing existed (checked against v4.3.4
with and without `--no-auth-key`). The framing request adds no new
incompatibility with them.

The health check (`dtailhealth`) never requests escaped framing: the server's
health handler ends the session after its first command and advertises no
capabilities.

## Where the code lives

* `internal/protocol/framing.go` — `Framing`, escaping and decoding
* `internal/protocol/wire.go` — `Framing.EncodeLine`, `Framing.EncodeMessage`,
  and `LineEncoder`, the server's per-line hot path, which escapes the
  constant header fields once per line writer (with escaped framing a typical
  line frame costs fewer instructions than the raw `EncodeLine` did before,
  which also allocated its line number string)
* `internal/protocol/capabilities.go` — capability and request constants
* `internal/handlers/session_framer.go` — the server session's framing state
* `internal/handlers/serverhandler.go` — handling of the request
* `internal/handlers/line_writer.go`, `protocol_formatter.go` — line frames
* `internal/clients/handlers/basehandler.go`, `maprhandler.go` — request and
  decoding on the client
* `integrationtests/utf8_framing_test.go` — server mode versus serverless
  dcat and dmap on `€`, `Ь`, `¬` and `丬`
