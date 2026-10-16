package protocol

import (
	"bytes"
	"testing"
)

// utf8WithDelimiterByte holds characters whose UTF-8 encoding contains the
// MessageDelimiter byte 0xAC: '€' (E2 82 AC), 'Ь' (D0 AC) and '¬' (C2 AC).
const utf8WithDelimiterByte = "price 5€ ok, soft Ь sign, not ¬ sign"

func TestEscapedFramingRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty", body: nil},
		{name: "ascii", body: []byte("INFO|plain ascii line\n")},
		{name: "utf8 with delimiter byte", body: []byte(utf8WithDelimiterByte + "\n")},
		{name: "only delimiters", body: []byte{MessageDelimiter, MessageDelimiter}},
		{name: "escape byte", body: []byte{'a', FrameEscape, 'b'}},
		{name: "escape byte before codes", body: []byte{FrameEscape, escapedDelimiterCode,
			FrameEscape, escapedEscapeCode}},
		{name: "escape then delimiter", body: []byte{FrameEscape, MessageDelimiter}},
		{name: "trailing escape byte", body: []byte{'x', FrameEscape}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var encoded bytes.Buffer
			EscapedFraming.EncodePayload(&encoded, test.body)
			frame := encoded.Bytes()
			if i := bytes.IndexByte(frame, MessageDelimiter); i != len(frame)-1 {
				t.Fatalf("delimiter at %d in %q, want only the terminating one", i, frame)
			}

			var scratch []byte
			decoded := EscapedFraming.DecodeFrame(frame[:len(frame)-1], &scratch)
			if !bytes.Equal(decoded, test.body) {
				t.Fatalf("decoded = %q, want %q", decoded, test.body)
			}

			var fromString bytes.Buffer
			EscapedFraming.WriteString(&fromString, string(test.body))
			if !bytes.Equal(fromString.Bytes(), frame[:len(frame)-1]) {
				t.Fatalf("WriteString = %q, Write = %q", fromString.Bytes(), frame[:len(frame)-1])
			}
		})
	}
}

func TestEscapedFramingKeepsTextWithoutSpecialBytesUnchanged(t *testing.T) {
	body := []byte("Grüße, こんにちは, ASCII|fields|123\n")
	var raw, escaped bytes.Buffer
	RawFraming.EncodePayload(&raw, body)
	EscapedFraming.EncodePayload(&escaped, body)
	if !bytes.Equal(raw.Bytes(), escaped.Bytes()) {
		t.Fatalf("escaped frame %q differs from raw frame %q", escaped.Bytes(), raw.Bytes())
	}

	frame := escaped.Bytes()[:escaped.Len()-1]
	var scratch []byte
	decoded := EscapedFraming.DecodeFrame(frame, &scratch)
	if &decoded[0] != &frame[0] {
		t.Fatal("DecodeFrame copied a frame without escape sequences")
	}
	if scratch != nil {
		t.Fatal("DecodeFrame used scratch for a frame without escape sequences")
	}
}

func TestRawFramingIsLegacyWireFormat(t *testing.T) {
	body := []byte(utf8WithDelimiterByte)
	var encoded bytes.Buffer
	RawFraming.EncodePayload(&encoded, body)
	want := append(append([]byte(nil), body...), MessageDelimiter)
	if !bytes.Equal(encoded.Bytes(), want) {
		t.Fatalf("raw frame = %q, want payload verbatim plus delimiter %q", encoded.Bytes(), want)
	}

	// Raw decoding never interprets escape sequences, so a raw frame from a
	// peer without escaped framing is passed on byte for byte.
	frame := []byte{'a', FrameEscape, escapedDelimiterCode}
	var scratch []byte
	if decoded := RawFraming.DecodeFrame(frame, &scratch); !bytes.Equal(decoded, frame) {
		t.Fatalf("raw DecodeFrame = %q, want %q", decoded, frame)
	}
}

func TestEscapedFramingDecodeKeepsMalformedSequences(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
	}{
		{name: "unknown code", frame: []byte{'a', FrameEscape, 'z', 'b'}},
		{name: "escape at end", frame: []byte{'a', FrameEscape}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var scratch []byte
			decoded := EscapedFraming.DecodeFrame(test.frame, &scratch)
			if !bytes.Equal(decoded, test.frame) {
				t.Fatalf("decoded = %q, want malformed frame kept verbatim %q", decoded, test.frame)
			}
		})
	}
}

func TestEscapedFramingDecodeDoesNotModifyFrame(t *testing.T) {
	var encoded bytes.Buffer
	EscapedFraming.EncodePayload(&encoded, []byte(utf8WithDelimiterByte))
	frame := encoded.Bytes()[:encoded.Len()-1]
	original := append([]byte(nil), frame...)

	var scratch []byte
	first := EscapedFraming.DecodeFrame(frame, &scratch)
	if string(first) != utf8WithDelimiterByte {
		t.Fatalf("decoded = %q, want %q", first, utf8WithDelimiterByte)
	}
	if !bytes.Equal(frame, original) {
		t.Fatalf("DecodeFrame modified its input: %q, want %q", frame, original)
	}
	if cap(scratch) == 0 {
		t.Fatal("DecodeFrame did not keep its scratch buffer for reuse")
	}
}

func TestEscapedFramingLineAndMessageFrames(t *testing.T) {
	var stream bytes.Buffer
	EscapedFraming.EncodeLine(&stream, Line{
		Hostname:           "host-a",
		TransmittedPercent: "100",
		Number:             7,
		SourceID:           "src€",
		Content:            []byte(utf8WithDelimiterByte + "\n"),
	})
	EscapedFraming.EncodeMessage(&stream, Message{
		Kind:     MessageAggregate,
		Hostname: "host-a",
		Content:  "group≔5€",
	})

	frames := bytes.Split(bytes.TrimSuffix(stream.Bytes(), []byte{MessageDelimiter}),
		[]byte{MessageDelimiter})
	if len(frames) != 2 {
		t.Fatalf("stream holds %d frames, want 2: %q", len(frames), stream.Bytes())
	}
	var scratch []byte
	line, err := DecodeLine(string(EscapedFraming.DecodeFrame(frames[0], &scratch)))
	if err != nil {
		t.Fatalf("DecodeLine: %v", err)
	}
	if line.Content != utf8WithDelimiterByte+"\n" || line.SourceID != "src€" {
		t.Fatalf("decoded line = %#v", line)
	}
	message, err := DecodeMessage(string(EscapedFraming.DecodeFrame(frames[1], &scratch)))
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if message.Kind != MessageAggregate || message.Content != "group≔5€" {
		t.Fatalf("decoded message = %#v", message)
	}
}

func BenchmarkFramingEncodeLine(b *testing.B) {
	line := Line{
		Hostname:           "serv-01",
		TransmittedPercent: "100",
		Number:             123456,
		SourceID:           "app.log",
		Content:            bytes.Repeat([]byte("INFO|1002-071143|1|stats.go:56|8|15|7|0.21|"), 4),
	}
	for _, framing := range []Framing{RawFraming, EscapedFraming} {
		b.Run(framing.String(), func(b *testing.B) {
			var dst bytes.Buffer
			for i := 0; i < b.N; i++ {
				dst.Reset()
				framing.EncodeLine(&dst, line)
			}
		})
		b.Run("encoder/"+framing.String(), func(b *testing.B) {
			encoder := framing.NewLineEncoder(line.Hostname, line.TransmittedPercent)
			var dst bytes.Buffer
			for i := 0; i < b.N; i++ {
				dst.Reset()
				encoder.Encode(&dst, line.Number, line.SourceID, line.Content)
			}
		})
	}
}

func TestLineEncoderMatchesEncodeLine(t *testing.T) {
	lines := []Line{
		{Hostname: "host-a", TransmittedPercent: "100", Number: 1, SourceID: "a.log",
			Content: []byte("first\n")},
		{Hostname: "host-a", TransmittedPercent: "100", Number: 1234567890, SourceID: "a.log",
			Content: []byte(utf8WithDelimiterByte)},
		{Hostname: "host-a", TransmittedPercent: "100", Number: 3, SourceID: "b€.log",
			Content: []byte{FrameEscape, MessageDelimiter}},
		{Hostname: "host-a", TransmittedPercent: "100", Number: 0, SourceID: "",
			Content: nil},
		{Hostname: "host-a", TransmittedPercent: "100", Number: 5, SourceID: "a.log",
			Content: []byte("again a\n")},
	}
	for _, framing := range []Framing{RawFraming, EscapedFraming} {
		encoder := framing.NewLineEncoder("host-a", "100")
		for _, line := range lines {
			var want, got bytes.Buffer
			framing.EncodeLine(&want, line)
			encoder.Encode(&got, line.Number, line.SourceID, line.Content)
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("%s: encoder frame %q, EncodeLine frame %q", framing, got.Bytes(), want.Bytes())
			}
		}
	}
}

func TestLineEncoderDoesNotAllocate(t *testing.T) {
	encoder := EscapedFraming.NewLineEncoder("host-a", "100")
	content := []byte(utf8WithDelimiterByte + "\n")
	var dst bytes.Buffer
	dst.Grow(4096)
	encoder.Encode(&dst, 1, "a.log", content)
	allocs := testing.AllocsPerRun(1000, func() {
		dst.Reset()
		encoder.Encode(&dst, 123456789, "a.log", content)
	})
	if allocs != 0 {
		t.Fatalf("Encode allocated %v objects per line, want 0", allocs)
	}
}

func BenchmarkFramingEncodePayload(b *testing.B) {
	ascii := bytes.Repeat([]byte("INFO|1002-071143|1|stats.go:56|8|15|7|0.21|"), 4)
	utf8 := bytes.Repeat([]byte(utf8WithDelimiterByte), 4)
	for _, bench := range []struct {
		name    string
		framing Framing
		body    []byte
	}{
		{"raw/ascii", RawFraming, ascii},
		{"escaped/ascii", EscapedFraming, ascii},
		{"escaped/utf8", EscapedFraming, utf8},
	} {
		b.Run(bench.name, func(b *testing.B) {
			var dst bytes.Buffer
			b.SetBytes(int64(len(bench.body)))
			for i := 0; i < b.N; i++ {
				dst.Reset()
				bench.framing.EncodePayload(&dst, bench.body)
			}
		})
	}
}
