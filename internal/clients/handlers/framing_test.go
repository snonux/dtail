package handlers

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
	maprclient "github.com/mimecast/dtail/internal/mapr/client"
	"github.com/mimecast/dtail/internal/protocol"
)

// framingTestLines contain characters whose UTF-8 encoding holds the
// MessageDelimiter byte 0xAC ('€', 'Ь', '¬') and the escape byte 0xFF.
var framingTestLines = []string{
	"price 5€ ok\n",
	"soft Ь sign\n",
	"not ¬ sign\n",
	"byte \xff\xac end\n",
}

func capabilitiesFrame(capabilities ...string) []byte {
	var stream bytes.Buffer
	protocol.RawFraming.EncodeMessage(&stream, protocol.Message{
		Kind:    protocol.MessagePlain,
		Content: protocol.HiddenCapabilitiesPrefix + strings.Join(capabilities, " "),
	})
	return stream.Bytes()
}

func payloadFrames(framing protocol.Framing, lines ...string) []byte {
	var stream bytes.Buffer
	for _, line := range lines {
		framing.EncodePayload(&stream, []byte(line))
	}
	return stream.Bytes()
}

func readFirstFrame(t *testing.T, handler *baseHandler) string {
	t.Helper()
	p := make([]byte, 4096)
	n, err := handler.Read(p)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	return string(p[:n])
}

func TestClientHandlersRequestEscapedFramingFirst(t *testing.T) {
	want := "protocol " + protocol.ProtocolCompat + " base64 " +
		base64.StdEncoding.EncodeToString([]byte(protocol.EscapedFramingCommand)) + ";"

	client := NewClientHandler("srv1", nil)
	go func() { _ = client.SendMessage("AUTHKEY abc") }()
	if got := readFirstFrame(t, &client.baseHandler); got != want {
		t.Fatalf("first client frame = %q, want the escaped framing request %q", got, want)
	}
	if got := readFirstFrame(t, &client.baseHandler); !strings.Contains(got, "base64") {
		t.Fatalf("second client frame = %q, want the AUTHKEY command", got)
	}

	query, err := mapr.NewQuery("select count(status) from stats group by status", logging.NopLogger{})
	if err != nil {
		t.Fatalf("NewQuery() error = %v", err)
	}
	mapClient := NewMaprHandler("srv1", maprclient.NewSessionState(query, logging.NopLogger{}), nil)
	if got := readFirstFrame(t, &mapClient.baseHandler); got != want {
		t.Fatalf("first mapr frame = %q, want the escaped framing request %q", got, want)
	}
}

// TestHealthHandlerDoesNotRequestEscapedFraming: the server's health handler
// ends the session after the first command, so a framing request must not
// precede the health command.
func TestHealthHandlerDoesNotRequestEscapedFraming(t *testing.T) {
	health := NewHealthHandler("srv1", nil)
	if health.escapedFramingRequested || len(health.pendingCommand) != 0 {
		t.Fatalf("health handler requested escaped framing: pending %q", health.pendingCommand)
	}
	if _, err := health.Write(capabilitiesFrame(protocol.CapabilityEscapedFramingV1)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if health.framing() != protocol.RawFraming {
		t.Fatal("health handler switched to escaped framing without requesting it")
	}
}

func TestClientHandlerDecodesFramesPerNegotiatedFraming(t *testing.T) {
	escapedLines := payloadFrames(protocol.EscapedFraming, framingTestLines...)
	tests := []struct {
		name         string
		request      bool
		capabilities []string
		payload      []byte
		want         string
	}{
		{
			name: "requested and advertised", request: true,
			capabilities: []string{protocol.CapabilityQueryUpdateV1, protocol.CapabilityEscapedFramingV1},
			payload:      escapedLines,
			want:         strings.Join(framingTestLines, ""),
		},
		{
			// An older server: raw frames cut lines at 0xAC, as before.
			name: "requested but not advertised", request: true,
			capabilities: []string{protocol.CapabilityQueryUpdateV1},
			payload:      payloadFrames(protocol.RawFraming, "price 5\xe2\x82", " ok\n"),
			want:         "price 5\xe2\x82\n ok\n",
		},
		{
			// Never decoded as escaped unless requested: the escape
			// sequences reach the output verbatim.
			name: "advertised but not requested", request: false,
			capabilities: []string{protocol.CapabilityEscapedFramingV1},
			payload:      payloadFrames(protocol.RawFraming, "a\xff\x01b\n"),
			want:         "a\xff\x01b\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &payloadRecorder{}
			handler := NewClientHandler("srv1", recorder)
			handler.escapedFramingRequested = test.request
			stream := append(capabilitiesFrame(test.capabilities...), test.payload...)
			for _, size := range []int{1, 3, len(stream)} {
				recorder.payload.Reset()
				handler.escapedFraming.Store(false)
				writeInChunks(t, handler, stream, size)
				if got := recorder.payload.String(); got != test.want {
					t.Fatalf("chunk size %d: payload = %q, want %q", size, got, test.want)
				}
			}
		})
	}
}

// TestClientHandlerDecodesFramesAfterCapabilitiesOnly: the capabilities frame
// itself is raw; frames before it (none in practice) are never unescaped.
func TestClientHandlerDecodesFramesAfterCapabilitiesOnly(t *testing.T) {
	recorder := &payloadRecorder{}
	handler := NewClientHandler("srv1", recorder)
	var stream []byte
	stream = append(stream, payloadFrames(protocol.RawFraming, "early \xff\x01\n")...)
	stream = append(stream, capabilitiesFrame(protocol.CapabilityEscapedFramingV1)...)
	stream = append(stream, payloadFrames(protocol.EscapedFraming, "late 5€\n")...)
	if _, err := handler.Write(stream); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got, want := recorder.payload.String(), "early \xff\x01\nlate 5€\n"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if !handler.WaitForCapabilities(time.Millisecond) ||
		!handler.HasCapability(protocol.CapabilityEscapedFramingV1) {
		t.Fatal("capabilities frame was not handled")
	}
}

func TestMaprHandlerDecodesEscapedAggregateFrames(t *testing.T) {
	query, err := mapr.NewQuery("select status,count(status) from stats group by status", logging.NopLogger{})
	if err != nil {
		t.Fatalf("NewQuery() error = %v", err)
	}
	session := maprclient.NewSessionState(query, logging.NopLogger{})
	logger := &recordingClientLogger{}
	handler := NewMaprHandler("srv1", session, logger)

	serialized := strings.Join([]string{
		"5€ ¬ Ь",
		"1",
		query.Select[0].FieldStorage + protocol.AggregateKVDelimiter + "5€ ¬ Ь",
		handlerCountStorage(t, query) + protocol.AggregateKVDelimiter + "3",
		"",
	}, protocol.AggregateDelimiter)
	var aggregate bytes.Buffer
	protocol.EscapedFraming.EncodeMessage(&aggregate, protocol.Message{
		Kind:     protocol.MessageAggregate,
		Hostname: "host1",
		Content:  serialized + "\n",
	})
	stream := append(capabilitiesFrame(protocol.CapabilityEscapedFramingV1), aggregate.Bytes()...)
	if _, writeErr := handler.Write(stream); writeErr != nil {
		t.Fatalf("Write() error = %v", writeErr)
	}
	handler.Shutdown()

	if len(logger.errors) != 0 {
		t.Fatalf("unexpected client errors: %q", logger.errors)
	}
	result, numRows, err := session.Snapshot().GlobalGroup.Result(query, 10, nil)
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if numRows != 1 || !strings.Contains(result, "5€ ¬ Ь") {
		t.Fatalf("aggregate result = %q (%d rows), want the group key 5€ ¬ Ь", result, numRows)
	}
}
