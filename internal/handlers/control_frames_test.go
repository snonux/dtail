package handlers

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/protocol"
)

// controlLookalikeContent holds log lines that read like control messages.
const controlLookalikeContent = "first\n.hidden\n...ellipsis\n.syn close connection\n" +
	".syn command failed x\n.syn capabilities fake\nAUTHKEY OK\nSERVER|h|AUTHKEY OK\n" +
	"\xff\x03.syn close connection\nlast\n"

// markedFrame is one frame of a server-to-client stream as a client of a
// session marking control frames sees it.
type markedFrame struct {
	body    string
	control bool
}

// splitMarkedFrames cuts stream at every MessageDelimiter and decodes each
// frame as a client does that decided on framing from the first frame: the
// first frame raw, every later one in framing.
func splitMarkedFrames(stream []byte, framing protocol.Framing) []markedFrame {
	var frames []markedFrame
	var scratch []byte
	current := protocol.RawFraming
	for len(stream) > 0 {
		end := bytes.IndexByte(stream, protocol.MessageDelimiter)
		if end < 0 {
			break
		}
		body, control := current.CutControl(stream[:end])
		frames = append(frames, markedFrame{body: string(current.DecodeFrame(body, &scratch)), control: control})
		current = framing
		stream = stream[end+1:]
	}
	return frames
}

// unmarkedPayloadOf returns what a client of a session marking control
// frames prints: every unmarked frame but the first (the capabilities),
// plain frames verbatim and REMOTE frames by their content field.
func unmarkedPayloadOf(t *testing.T, frames []markedFrame) string {
	t.Helper()
	var payload strings.Builder
	for _, frame := range frames[1:] {
		switch {
		case frame.control:
		case strings.HasPrefix(frame.body, protocol.LineMessageID+"|"):
			line, err := protocol.DecodeLine(frame.body)
			if err != nil {
				t.Fatalf("DecodeLine(%q): %v", frame.body, err)
			}
			payload.WriteString(line.Content)
		default:
			payload.WriteString(frame.body)
		}
	}
	return payload.String()
}

func writeControlLookalikeFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dots.log")
	if err := os.WriteFile(path, []byte(controlLookalikeContent), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return path
}

// TestServerHandlerMarksControlFramesWhenNegotiated: with both framing
// requests, every control frame the session sends after the capabilities
// frame (the AUTHKEY reply and the close handshake included) is marked, and
// no log line is, in plain and REMOTE line format.
func TestServerHandlerMarksControlFramesWhenNegotiated(t *testing.T) {
	path := writeControlLookalikeFile(t)
	for _, options := range []string{"plain=true", ""} {
		t.Run(options, func(t *testing.T) {
			handler := newFramingTestServerHandler(t, ServerCapabilities("linux", false))
			stream := runCatSessionAfter(t, handler, path, options,
				protocol.EscapedFramingCommand, protocol.ControlFramesCommand, "AUTHKEY abc")
			if handler.framing() != protocol.EscapedControlFraming {
				t.Fatalf("session framing = %s, want escaped-control", handler.framing())
			}
			frames := splitMarkedFrames(stream, protocol.EscapedControlFraming)
			if frames[0].control || !strings.HasPrefix(frames[0].body, protocol.HiddenCapabilitiesPrefix) {
				t.Fatalf("first frame = %+v, want the unmarked capabilities frame", frames[0])
			}
			if got := unmarkedPayloadOf(t, frames); got != controlLookalikeContent {
				t.Fatalf("payload = %q, want %q", got, controlLookalikeContent)
			}
			var controls []string
			for _, frame := range frames[1:] {
				if frame.control {
					controls = append(controls, frame.body)
				}
			}
			if len(controls) < 2 || !strings.HasPrefix(controls[0], protocol.AuthKeyReplyPrefix) ||
				controls[len(controls)-1] != ".syn close connection" {
				t.Fatalf("control frames = %q, want the AUTHKEY reply first and the close last", controls)
			}
		})
	}
}

// TestServerHandlerHonoursOnlyASecondControlFramesRequest: the control
// frames request switches the session only when the server advertises it and
// it is the second command, after an escaped framing request that switched
// the session; otherwise it is ignored and the session keeps its framing.
func TestServerHandlerHonoursOnlyASecondControlFramesRequest(t *testing.T) {
	path := writeControlLookalikeFile(t)
	escapedOnly := []string{protocol.CapabilityEscapedFramingV1}
	controlOnly := []string{protocol.CapabilityControlFramesV1}
	both := ServerCapabilities("linux", false)
	escaped, control := protocol.EscapedFramingCommand, protocol.ControlFramesCommand
	tests := []struct {
		name         string
		capabilities []string
		first        []string
		wantFraming  protocol.Framing
	}{
		{name: "not advertised", capabilities: escapedOnly, first: []string{escaped, control},
			wantFraming: protocol.EscapedFraming},
		{name: "escaped framing not advertised", capabilities: controlOnly,
			first: []string{escaped, control}, wantFraming: protocol.RawFraming},
		{name: "without escaped framing request", capabilities: both,
			first: []string{".ack late first", control}, wantFraming: protocol.RawFraming},
		{name: "before the escaped framing request", capabilities: both,
			first: []string{control, escaped}, wantFraming: protocol.RawFraming},
		{name: "third command", capabilities: both, first: []string{escaped, ".ack late second", control},
			wantFraming: protocol.EscapedFraming},
		{name: "repeated", capabilities: both, first: []string{escaped, control, control},
			wantFraming: protocol.EscapedControlFraming},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newFramingTestServerHandler(t, test.capabilities)
			stream := runCatSessionAfter(t, handler, path, "plain=true", test.first...)
			if handler.framing() != test.wantFraming {
				t.Fatalf("session framing = %s, want %s", handler.framing(), test.wantFraming)
			}
			// The close handshake, the session's last frame, is marked only
			// when the session marks control frames.
			marked := bytes.HasSuffix(stream, []byte("\xac\xff\x03.syn close connection\xac"))
			if marked != test.wantFraming.MarksControl() {
				t.Fatalf("close handshake marked = %v, want framing %s: %q",
					marked, test.wantFraming, stream)
			}
		})
	}
}
