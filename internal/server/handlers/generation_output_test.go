package handlers

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/io/line"
	userserver "github.com/mimecast/dtail/internal/user/server"
)

func TestDecodeGeneratedMessage(t *testing.T) {
	generation, message := decodeGeneratedMessage(encodeGeneratedMessage(7, "hello"))
	if generation != 7 {
		t.Fatalf("unexpected generation: %d", generation)
	}
	if message != "hello" {
		t.Fatalf("unexpected message: %q", message)
	}
}

func TestBaseHandlerReadDropsStaleServerMessage(t *testing.T) {
	handler := newGenerationTestHandler(2)
	handler.serverMessages <- encodeGeneratedMessage(1, "stale\n")
	handler.serverMessages <- encodeGeneratedMessage(2, "fresh\n")

	got := readHandlerOutput(t, &handler)
	if strings.Contains(got, "stale") {
		t.Fatalf("unexpected stale output: %q", got)
	}
	if !strings.Contains(got, "fresh") {
		t.Fatalf("expected current output, got %q", got)
	}
}

func TestBaseHandlerReadDropsStaleMaprMessage(t *testing.T) {
	handler := newGenerationTestHandler(3)
	handler.maprMessages <- encodeGeneratedMessage(2, "old aggregate")
	handler.maprMessages <- encodeGeneratedMessage(3, "new aggregate")

	got := readHandlerOutput(t, &handler)
	if strings.Contains(got, "old aggregate") {
		t.Fatalf("unexpected stale aggregate output: %q", got)
	}
	if !strings.Contains(got, "new aggregate") {
		t.Fatalf("expected current aggregate output, got %q", got)
	}
}

func TestBaseHandlerReadDropsStaleLine(t *testing.T) {
	handler := newGenerationTestHandler(4)

	staleLine := line.New(bytes.NewBufferString("stale line"), 1, 100, "app.log")
	staleLine.Generation = 3
	currentLine := line.New(bytes.NewBufferString("fresh line"), 2, 100, "app.log")
	currentLine.Generation = 4

	handler.lines <- staleLine
	handler.lines <- currentLine

	got := readHandlerOutput(t, &handler)
	if strings.Contains(got, "stale line") {
		t.Fatalf("unexpected stale line output: %q", got)
	}
	if !strings.Contains(got, "fresh line") {
		t.Fatalf("expected current line output, got %q", got)
	}
}

func TestTurboManagerTryReadDropsStaleGeneration(t *testing.T) {
	resetServerLogger(t)

	manager := turboManager{
		mode:  true,
		lines: make(chan []byte, 2),
	}
	manager.lines <- encodeGeneratedBytes(1, []byte("stale"))
	manager.lines <- encodeGeneratedBytes(2, []byte("fresh"))

	buf := make([]byte, 32)
	n, handled := manager.tryRead(buf, &userserver.User{Name: "turbo-test"}, func(generation uint64) bool {
		return generation != 0 && generation != 2
	})
	if !handled {
		t.Fatalf("expected turbo read to be handled")
	}
	if got := string(buf[:n]); got != "fresh" {
		t.Fatalf("unexpected turbo output: %q", got)
	}
}

func newGenerationTestHandler(activeGeneration uint64) baseHandler {
	return baseHandler{
		done:           internal.NewDone(),
		lines:          make(chan *line.Line, 2),
		serverMessages: make(chan string, 2),
		maprMessages:   make(chan string, 2),
		hostname:       "testhost",
		activeGeneration: func() uint64 {
			return activeGeneration
		},
	}
}

func readHandlerOutput(t *testing.T, handler *baseHandler) string {
	t.Helper()

	buf := make([]byte, 256)
	n, err := handler.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	return string(buf[:n])
}
