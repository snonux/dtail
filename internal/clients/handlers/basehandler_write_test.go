package handlers

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/clients/clientlog"
	"github.com/mimecast/dtail/internal/protocol"
)

// payloadRecorder is a client logger that records every payload write and
// whether it arrived through the byte-slice or the string path. It copies the
// bytes it receives, as a real sink does, so tests notice a handler that
// hands out a slice it later changes.
type payloadRecorder struct {
	clientlog.NopLogger
	payload    bytes.Buffer
	writes     []string
	stringRaws int
	warnings   []string
}

func (r *payloadRecorder) RawBytes(message []byte) {
	r.payload.Write(message)
	r.writes = append(r.writes, string(message))
}

func (r *payloadRecorder) Raw(message string) string {
	r.stringRaws++
	r.payload.WriteString(message)
	r.writes = append(r.writes, message)
	return message
}

func (r *payloadRecorder) Warn(args ...any) string {
	message := fmt.Sprint(args...)
	r.warnings = append(r.warnings, message)
	return message
}

// legacyPayload is the output the previous byte-by-byte receive loop produced
// for a stream of payload-only messages: every complete message is printed
// with exactly one trailing newline, an unterminated tail is not printed.
func legacyPayload(stream []byte) (payload string, messages int) {
	var out strings.Builder
	parts := bytes.Split(stream, []byte{protocol.MessageDelimiter})
	for _, message := range parts[:len(parts)-1] {
		out.Write(message)
		if len(message) == 0 || message[len(message)-1] != '\n' {
			out.WriteByte('\n')
		}
		messages++
	}
	return out.String(), messages
}

func frame(messages ...string) []byte {
	var stream []byte
	for _, message := range messages {
		stream = append(stream, message...)
		stream = append(stream, protocol.MessageDelimiter)
	}
	return stream
}

func writeInChunks(t *testing.T, handler *ClientHandler, stream []byte, size int) {
	t.Helper()
	for start := 0; start < len(stream); start += size {
		end := min(start+size, len(stream))
		n, err := handler.Write(stream[start:end])
		if err != nil || n != end-start {
			t.Fatalf("Write() = %d, %v; want %d, nil", n, err, end-start)
		}
	}
}

// TestBaseHandlerWriteIsIndependentOfChunking feeds the same stream in chunks
// of many sizes, so delimiters land at every buffer position: first byte, last
// byte, and split between chunks. Output must match the legacy loop byte for
// byte, and each message must reach the sink in exactly one call so a line
// cannot be separated from its own newline by output from another server.
func TestBaseHandlerWriteIsIndependentOfChunking(t *testing.T) {
	longLine := strings.Repeat("0123456789abcdef", 12*1024) // 192 KiB, above bufio and io.Copy sizes
	stream := frame(
		"first line\n",
		"no trailing newline",
		"",
		"\n",
		"\n\n",
		"multi\nline\nmessage",
		longLine,
		longLine+"\n",
		"REMOTE|host|100|1|src|content with | pipes\n",
		"last\n",
	)
	want, wantMessages := legacyPayload(stream)

	for _, size := range []int{1, 2, 3, 7, 13, 31, 4096, 32 * 1024, len(stream)} {
		t.Run(fmt.Sprintf("chunk%d", size), func(t *testing.T) {
			recorder := &payloadRecorder{}
			handler := NewClientHandler("srv1", recorder)

			writeInChunks(t, handler, stream, size)

			if got := recorder.payload.String(); got != want {
				t.Fatalf("payload differs from legacy output: got %d bytes, want %d bytes", len(got), len(want))
			}
			if len(recorder.writes) != wantMessages {
				t.Fatalf("sink calls = %d, want one per message (%d)", len(recorder.writes), wantMessages)
			}
			if recorder.stringRaws != 0 {
				t.Fatalf("payload used the string Raw path %d times, want the byte path", recorder.stringRaws)
			}
		})
	}
}

// TestBaseHandlerWriteKeepsUnterminatedTail checks that a truncated message is
// never printed on its own and is completed by the next chunk.
func TestBaseHandlerWriteKeepsUnterminatedTail(t *testing.T) {
	recorder := &payloadRecorder{}
	handler := NewClientHandler("srv1", recorder)

	writeInChunks(t, handler, []byte("partial"), 64)
	if recorder.payload.Len() != 0 {
		t.Fatalf("unterminated message was printed: %q", recorder.payload.String())
	}

	chunk := append([]byte(" rest"), protocol.MessageDelimiter)
	chunk = append(chunk, "tail"...)
	writeInChunks(t, handler, chunk, 64)
	if got, want := recorder.payload.String(), "partial rest\n"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}

	writeInChunks(t, handler, frame("\n"), 64)
	if got, want := recorder.payload.String(), "partial rest\ntail\n"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

// TestBaseHandlerWriteConsumesControlMessages checks that hidden messages and
// AUTHKEY acknowledgements never reach the payload sink, while look-alikes do.
func TestBaseHandlerWriteConsumesControlMessages(t *testing.T) {
	stream := frame(
		".syn capabilities query-update-v1",
		"AUTHKEY OK",
		"SERVER|srv1|AUTHKEY ERR invalid base64\n",
		"  AUTHKEY OK \n",
		"payload\n",
		"SERVER|srv1|some server info\n",
		"AUTHKEYS are not acknowledgements",
		" .not hidden because of the leading space",
		"REMOTE|host|100|1|src|AUTHKEY OK\n",
	)
	want := "payload\n" +
		"SERVER|srv1|some server info\n" +
		"AUTHKEYS are not acknowledgements\n" +
		" .not hidden because of the leading space\n" +
		"REMOTE|host|100|1|src|AUTHKEY OK\n"

	for _, size := range []int{1, 5, len(stream)} {
		t.Run(fmt.Sprintf("chunk%d", size), func(t *testing.T) {
			recorder := &payloadRecorder{}
			handler := NewClientHandler("srv1", recorder)

			writeInChunks(t, handler, stream, size)

			if got := recorder.payload.String(); got != want {
				t.Fatalf("payload = %q, want %q", got, want)
			}
			if !handler.HasCapability(protocol.CapabilityQueryUpdateV1) {
				t.Fatalf("hidden capabilities message was not handled")
			}
			if len(recorder.warnings) != 1 || !strings.Contains(recorder.warnings[0], "invalid base64") {
				t.Fatalf("warnings = %q, want one AUTHKEY failure with its detail", recorder.warnings)
			}
		})
	}
}

// TestBaseHandlerWriteDoesNotModifyInput guards the io.Writer contract: the
// newline for a message without one must not be written into p.
func TestBaseHandlerWriteDoesNotModifyInput(t *testing.T) {
	handler := NewClientHandler("srv1", &payloadRecorder{})
	input := frame("no newline", "second", "unterminated")
	input = input[:len(input)-1]
	original := bytes.Clone(input)

	if _, err := handler.Write(input); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !bytes.Equal(input, original) {
		t.Fatalf("Write modified its input:\ngot  %q\nwant %q", input, original)
	}
}

// TestBaseHandlerWritePayloadDoesNotAllocate checks the steady state of the
// hot path: complete payload lines, with and without a trailing newline and
// with a message split across chunks, cost no heap allocations.
func TestBaseHandlerWritePayloadDoesNotAllocate(t *testing.T) {
	handler := NewClientHandler("srv1", clientlog.NopLogger{})
	stream := frame("with newline\n", "without newline", "REMOTE|host|100|1|src|line\n")
	stream = append(stream, "split across"...)
	next := frame(" chunks\n")

	write := func() {
		_, _ = handler.Write(stream)
		_, _ = handler.Write(next)
	}
	write() // grow receiveBuf and lineBuf once

	if allocs := testing.AllocsPerRun(100, write); allocs != 0 {
		t.Fatalf("Write allocated %.1f times per run, want 0", allocs)
	}
}

// TestMaprHandlerWritePrintsNonAggregatePayload locks in the mapr handler's
// newline handling after it switched to the byte-slice message path.
func TestMaprHandlerWritePrintsNonAggregatePayload(t *testing.T) {
	recorder := &payloadRecorder{}
	handler := &MaprHandler{baseHandler: baseHandler{server: "srv1", logger: recorder}}

	if _, err := handler.Write(frame("SERVER|srv1|hello\n", "plain", "a\nb", "AUTHKEY OK")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got, want := recorder.payload.String(), "SERVER|srv1|hello\nplain\nab\n"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestMayBeAuthKeyMessage(t *testing.T) {
	tests := []struct {
		message string
		want    bool
	}{
		{message: "AUTHKEY OK", want: true},
		{message: " \tAUTHKEY ERR detail\n", want: true},
		{message: "SERVER|host|AUTHKEY OK\n", want: true},
		{message: "AGGREGATE|host|AUTHKEY OK", want: true},
		{message: "", want: false},
		{message: "\n", want: false},
		{message: "2026-01-01T00:00:00Z INFO request=000000001 status=200\n", want: false},
		{message: "REMOTE|host|100|1|src|AUTHKEY OK\n", want: false},
		{message: "SERVER", want: false},
		{message: "AUTHKE", want: false},
	}
	for _, tc := range tests {
		if got := mayBeAuthKeyMessage([]byte(tc.message)); got != tc.want {
			t.Errorf("mayBeAuthKeyMessage(%q) = %v, want %v", tc.message, got, tc.want)
		}
	}
}

// FuzzMayBeAuthKeyMessage proves the pre-check is safe: it may let through
// messages that are not acknowledgements, but it must never reject one that
// parseAuthKeyMessage accepts. The seeds include Unicode white space, which
// TrimSpace removes on both sides of the comparison.
func FuzzMayBeAuthKeyMessage(f *testing.F) {
	for _, seed := range []string{
		"AUTHKEY OK", "AUTHKEY ERR x", "SERVER|h|AUTHKEY OK", "AGGREGATE|h| AUTHKEY ERR\n",
		" AUTHKEY OK ", "SERVER|AUTHKEY OK", "SERVER||AUTHKEY OK", "payload\n", "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, message string) {
		isAuthKey, _, _ := parseAuthKeyMessage(message)
		if isAuthKey && !mayBeAuthKeyMessage([]byte(message)) {
			t.Fatalf("pre-check rejected authkey message %q", message)
		}
	})
}

func BenchmarkBaseHandlerWrite(b *testing.B) {
	handler := NewClientHandler("srv1", clientlog.NopLogger{})
	line := "2026-01-01T00:00:00Z INFO request=000000001 user001 path=/api/item/0001 status=200 payload=abcdefghijklmnopqrstuvwxyz0123456789\n"
	var chunk []byte
	for len(chunk) < 32*1024 {
		chunk = append(chunk, line...)
		chunk = append(chunk, protocol.MessageDelimiter)
	}
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	for b.Loop() {
		_, _ = handler.Write(chunk)
	}
}
