package protocol

import (
	"bytes"
	"strings"
)

// Framing selects how the body of a frame is written between two
// MessageDelimiter bytes on the server-to-client stream.
//
// MessageDelimiter is the single byte 0xAC. That byte is also a UTF-8
// continuation byte: it occurs in every character whose code point is 0x2C
// modulo 64, for example '¬' (C2 AC), '€' (E2 82 AC) and 'Ь' (D0 AC). Raw
// framing therefore cuts a log line containing such a character into two
// messages and loses the 0xAC byte. Escaped framing removes every 0xAC byte
// from frame bodies, so MessageDelimiter only ever terminates a frame.
//
// Which framing a session uses is negotiated per session so that clients and
// servers of different versions keep working together (see
// CapabilityEscapedFramingV1 and EscapedFramingCommand): only when the server
// advertises the capability and the client asks for it are frames escaped.
type Framing uint8

const (
	// RawFraming writes frame bodies verbatim. It is the legacy framing of
	// protocol 4.1 and stays in use with every peer that did not negotiate
	// EscapedFraming.
	RawFraming Framing = iota
	// EscapedFraming replaces each MessageDelimiter byte of a frame body with
	// FrameEscape followed by escapedDelimiterCode, and each FrameEscape byte
	// with FrameEscape followed by escapedEscapeCode. All other bytes, and so
	// all UTF-8 text without 0xAC, stay unchanged.
	EscapedFraming
)

const (
	// FrameEscape introduces an escape sequence in an EscapedFraming body.
	// 0xFF never occurs in valid UTF-8, so escaping it costs nothing for
	// UTF-8 logs and keeps the escaped body of ASCII frames byte-identical to
	// the raw one.
	FrameEscape byte = 0xFF
	// escapedDelimiterCode follows FrameEscape in place of a body byte equal
	// to MessageDelimiter.
	escapedDelimiterCode byte = 0x01
	// escapedEscapeCode follows FrameEscape in place of a body byte equal to
	// FrameEscape.
	escapedEscapeCode byte = 0x02
	// shortBodyLen is the length up to which a body is scanned for bytes to
	// escape in one plain loop instead of two vectorised IndexByte calls.
	shortBodyLen = 32
)

// Write appends body bytes of a frame to dst in framing f. It does not
// terminate the frame.
func (f Framing) Write(dst *bytes.Buffer, body []byte) {
	if f != EscapedFraming {
		dst.Write(body)
		return
	}
	for len(body) > 0 {
		i := indexEscapable(body)
		if i < 0 {
			dst.Write(body)
			return
		}
		dst.Write(body[:i])
		writeEscapeSequence(dst, body[i])
		body = body[i+1:]
	}
}

// WriteString appends body bytes of a frame to dst in framing f. It does
// not terminate the frame.
func (f Framing) WriteString(dst *bytes.Buffer, body string) {
	if f != EscapedFraming {
		dst.WriteString(body)
		return
	}
	for len(body) > 0 {
		i := indexEscapableString(body)
		if i < 0 {
			dst.WriteString(body)
			return
		}
		dst.WriteString(body[:i])
		writeEscapeSequence(dst, body[i])
		body = body[i+1:]
	}
}

// EncodePayload appends an untagged frame whose body is payload, e.g. a
// plain-mode log line.
func (f Framing) EncodePayload(dst *bytes.Buffer, payload []byte) {
	f.Write(dst, payload)
	dst.WriteByte(MessageDelimiter)
}

// DecodeFrame returns the body of a frame received in framing f, with its
// MessageDelimiter already removed. When the frame holds no escape sequence
// (always under RawFraming) it is returned as it is, without copying, and
// frame is never modified; otherwise the decoded body is built in *scratch,
// which keeps the grown buffer for the next frame, and the result is only
// valid until scratch is used again. Malformed sequences (an unknown code or
// a FrameEscape at the end) are kept verbatim; no encoder produces them.
func (f Framing) DecodeFrame(frame []byte, scratch *[]byte) []byte {
	if f != EscapedFraming || bytes.IndexByte(frame, FrameEscape) < 0 {
		return frame
	}
	decoded := (*scratch)[:0]
	for {
		i := bytes.IndexByte(frame, FrameEscape)
		if i < 0 || i+1 == len(frame) {
			decoded = append(decoded, frame...)
			break
		}
		decoded = append(decoded, frame[:i]...)
		switch frame[i+1] {
		case escapedDelimiterCode:
			decoded = append(decoded, MessageDelimiter)
		case escapedEscapeCode:
			decoded = append(decoded, FrameEscape)
		default:
			decoded = append(decoded, frame[i], frame[i+1])
		}
		frame = frame[i+2:]
	}
	*scratch = decoded
	return decoded
}

// String returns the name of the framing for diagnostics.
func (f Framing) String() string {
	if f == EscapedFraming {
		return "escaped"
	}
	return "raw"
}

// writeEscapeSequence appends the escape sequence of a body byte that is
// MessageDelimiter or FrameEscape.
func writeEscapeSequence(dst *bytes.Buffer, b byte) {
	dst.WriteByte(FrameEscape)
	if b == MessageDelimiter {
		dst.WriteByte(escapedDelimiterCode)
		return
	}
	dst.WriteByte(escapedEscapeCode)
}

// indexEscapable returns the index of the first byte of body that must be
// escaped, or -1. Each byte is scanned at most twice with the vectorised
// IndexByte, so text without such bytes stays at memchr speed; short bodies
// such as header fields are scanned in one plain loop, which is cheaper than
// two IndexByte calls there.
func indexEscapable(body []byte) int {
	if len(body) <= shortBodyLen {
		return indexEscapableShort(body)
	}
	delimiter := bytes.IndexByte(body, MessageDelimiter)
	head := body
	if delimiter >= 0 {
		head = body[:delimiter]
	}
	if escape := bytes.IndexByte(head, FrameEscape); escape >= 0 {
		return escape
	}
	return delimiter
}

// indexEscapableString is indexEscapable for string bodies.
func indexEscapableString(body string) int {
	if len(body) <= shortBodyLen {
		return indexEscapableShortString(body)
	}
	delimiter := strings.IndexByte(body, MessageDelimiter)
	head := body
	if delimiter >= 0 {
		head = body[:delimiter]
	}
	if escape := strings.IndexByte(head, FrameEscape); escape >= 0 {
		return escape
	}
	return delimiter
}

// indexEscapableShort is the single-loop scan of indexEscapable for bodies of
// at most shortBodyLen bytes.
func indexEscapableShort(body []byte) int {
	for i, b := range body {
		if b == MessageDelimiter || b == FrameEscape {
			return i
		}
	}
	return -1
}

// indexEscapableShortString is indexEscapableShort for string bodies.
func indexEscapableShortString(body string) int {
	for i := 0; i < len(body); i++ {
		if b := body[i]; b == MessageDelimiter || b == FrameEscape {
			return i
		}
	}
	return -1
}
