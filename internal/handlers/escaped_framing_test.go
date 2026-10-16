package handlers

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/authkey"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/session"
	userserver "github.com/mimecast/dtail/internal/sessionuser"
)

// framingTestContent holds characters whose UTF-8 encoding contains the
// MessageDelimiter byte 0xAC ('€', 'Ь', '¬'), and a raw 0xFF byte.
const framingTestContent = "price 5€ ok\nsoft Ь sign\nnot ¬ sign\nbyte \xff\xac end\n"

// splitFrames cuts a server-to-client stream at every MessageDelimiter and
// decodes each frame in framing, as a client receive loop does.
func splitFrames(stream []byte, framing protocol.Framing) []string {
	var frames []string
	var scratch []byte
	for len(stream) > 0 {
		end := bytes.IndexByte(stream, protocol.MessageDelimiter)
		if end < 0 {
			break
		}
		frames = append(frames, string(framing.DecodeFrame(stream[:end], &scratch)))
		stream = stream[end+1:]
	}
	return frames
}

func TestSessionFramerSwitchesToEscapedFramingAfterCapabilitiesFrame(t *testing.T) {
	handler := newReadTestHandler()
	capabilities := protocol.HiddenCapabilitiesPrefix + protocol.CapabilityEscapedFramingV1
	handler.serverMessages <- capabilities

	enabled := make(chan bool, 1)
	go func() { enabled <- handler.enableEscapedFraming(context.Background()) }()
	select {
	case <-enabled:
		t.Fatal("escaped framing was enabled before the capabilities frame was encoded")
	case <-time.After(50 * time.Millisecond):
	}

	if got := splitFrames(readExactly(t, handler, 4096, len(capabilities)+1),
		protocol.RawFraming); len(got) != 1 || got[0] != capabilities {
		t.Fatalf("capabilities frame = %q, want raw %q", got, capabilities)
	}
	select {
	case ok := <-enabled:
		if !ok {
			t.Fatal("enableEscapedFraming() = false, want true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("escaped framing was not enabled after the capabilities frame")
	}

	handler.serverMessages <- "server 5€"
	handler.maprMessages <- "group≔Ь¬"
	var want bytes.Buffer
	protocol.EscapedFraming.EncodeMessage(&want, protocol.Message{
		Kind: protocol.MessageServer, Hostname: "testhost", Content: "server 5€"})
	protocol.EscapedFraming.EncodeMessage(&want, protocol.Message{
		Kind: protocol.MessageAggregate, Hostname: "testhost", Content: "group≔Ь¬"})
	stream := readExactly(t, handler, 4096, want.Len())
	if !bytes.Equal(stream, want.Bytes()) {
		t.Fatalf("escaped frames = %q, want %q", stream, want.Bytes())
	}
	frames := splitFrames(stream, protocol.EscapedFraming)
	if len(frames) != 2 || frames[0] != "SERVER|testhost|server 5€" ||
		frames[1] != "AGGREGATE|testhost|group≔Ь¬" {
		t.Fatalf("decoded frames = %q", frames)
	}
}

func TestSessionFramerEnableEscapedFramingGivesUpWithContext(t *testing.T) {
	handler := newReadTestHandler()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if handler.enableEscapedFraming(ctx) {
		t.Fatal("enableEscapedFraming() = true without an encoded capabilities frame")
	}
	if handler.framing() != protocol.RawFraming {
		t.Fatal("session left raw framing although the switch gave up")
	}
}

func newFramingTestServerHandler(t *testing.T, capabilities []string) *ServerHandler {
	t.Helper()
	handler, err := NewServerHandler(context.Background(), &userserver.User{Name: config.ContinuousUser},
		Dependencies{
			CatLimiter:   make(chan struct{}, 4),
			TailLimiter:  make(chan struct{}, 4),
			ServerConfig: &config.ServerConfig{MapreduceLogFormat: "default", AuthKeyEnabled: true},
			AuthKeyStore: authkey.New(time.Hour, 5),
			Loggers:      HandlerLoggers{Diagnostics: handlerTestLogger, Reader: handlerTestLogger},
			Capabilities: capabilities,
			Hostname:     "testhost",
		})
	if err != nil {
		t.Fatalf("NewServerHandler: %v", err)
	}
	return handler
}

// runFramingCatSession runs one cat session, with the escaped framing
// request first when requested, and returns all bytes sent to the client.
func runFramingCatSession(t *testing.T, handler *ServerHandler, path, options string,
	request bool) []byte {
	t.Helper()
	spec := session.Spec{Mode: omode.CatClient, Files: []string{path}, Options: options, Regex: "."}
	commands, err := spec.Commands()
	if err != nil {
		t.Fatalf("build commands: %v", err)
	}
	if request {
		commands = append([]string{protocol.EscapedFramingCommand}, commands...)
	}

	commandWg := wrapHandlerCommandsForJoin(handler)
	output := &testOutput{}
	var writeMu sync.Mutex
	readerDone := startTestReader(handler, output, &writeMu)
	var frames strings.Builder
	for _, command := range commands {
		frames.WriteString(encodeTestCommand(command))
	}
	writeMu.Lock()
	_, writeErr := handler.Write([]byte(frames.String()))
	writeMu.Unlock()
	if writeErr != nil {
		t.Fatalf("write commands: %v", writeErr)
	}
	select {
	case <-handler.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("session did not shut down after the cat command finished")
	}
	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not observe EOF after handler shutdown")
	}
	waitForCommandJoin(t, commandWg, 10*time.Second)
	return []byte(output.String())
}

// payloadOf returns the log content a client prints from decoded frames:
// plain frames verbatim, REMOTE frames by their content field.
func payloadOf(t *testing.T, frames []string) string {
	t.Helper()
	var payload strings.Builder
	for _, frame := range frames {
		switch {
		case strings.HasPrefix(frame, "."):
		case strings.HasPrefix(frame, protocol.LineMessageID+"|"):
			line, err := protocol.DecodeLine(frame)
			if err != nil {
				t.Fatalf("DecodeLine(%q): %v", frame, err)
			}
			payload.WriteString(line.Content)
		default:
			payload.WriteString(frame)
		}
	}
	return payload.String()
}

func TestServerHandlerEscapesFramesOnlyWhenNegotiated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf8.log")
	if err := os.WriteFile(path, []byte(framingTestContent), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	advertised := ServerCapabilities("linux", false)
	tests := []struct {
		name         string
		capabilities []string
		request      bool
		wantFraming  protocol.Framing
	}{
		{name: "advertised and requested", capabilities: advertised, request: true,
			wantFraming: protocol.EscapedFraming},
		{name: "advertised, older client", capabilities: advertised, request: false,
			wantFraming: protocol.RawFraming},
		{name: "requested, not advertised", capabilities: nil, request: true,
			wantFraming: protocol.RawFraming},
	}
	for _, test := range tests {
		for _, options := range []string{"plain=true", ""} {
			t.Run(test.name+"/"+options, func(t *testing.T) {
				handler := newFramingTestServerHandler(t, test.capabilities)
				stream := runFramingCatSession(t, handler, path, options, test.request)
				frames := splitFrames(stream, test.wantFraming)
				got := payloadOf(t, frames)
				if test.wantFraming == protocol.EscapedFraming && got != framingTestContent {
					t.Fatalf("payload = %q, want %q", got, framingTestContent)
				}
				if test.wantFraming == protocol.RawFraming &&
					bytes.Contains(stream, []byte{protocol.FrameEscape, 0x01}) {
					t.Fatalf("raw session sent an escape sequence: %q", stream)
				}
				if test.wantFraming == protocol.RawFraming && got == framingTestContent {
					t.Fatal("raw session unexpectedly kept lines with 0xAC whole")
				}
			})
		}
	}
}
