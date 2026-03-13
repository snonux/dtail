package handlers

import (
	"fmt"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/protocol"
)

func TestParseAuthKeyMessage(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		wantAuth bool
		wantOK   bool
		wantInfo string
	}{
		{
			name:     "server formatted success",
			message:  fmt.Sprintf("SERVER%s%s%sAUTHKEY OK\n", protocol.FieldDelimiter, "host1", protocol.FieldDelimiter),
			wantAuth: true,
			wantOK:   true,
		},
		{
			name:     "server formatted error",
			message:  fmt.Sprintf("SERVER%s%s%sAUTHKEY ERR feature disabled\n", protocol.FieldDelimiter, "host1", protocol.FieldDelimiter),
			wantAuth: true,
			wantOK:   false,
			wantInfo: "feature disabled",
		},
		{
			name:     "plain response success",
			message:  "AUTHKEY OK",
			wantAuth: true,
			wantOK:   true,
		},
		{
			name:     "not an authkey message",
			message:  fmt.Sprintf("SERVER%s%s%ssome other message", protocol.FieldDelimiter, "host1", protocol.FieldDelimiter),
			wantAuth: false,
			wantOK:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotAuth, gotOK, gotInfo := parseAuthKeyMessage(tc.message)
			if gotAuth != tc.wantAuth {
				t.Fatalf("Unexpected auth marker: got %v want %v", gotAuth, tc.wantAuth)
			}
			if gotOK != tc.wantOK {
				t.Fatalf("Unexpected ok marker: got %v want %v", gotOK, tc.wantOK)
			}
			if gotInfo != tc.wantInfo {
				t.Fatalf("Unexpected info: got %q want %q", gotInfo, tc.wantInfo)
			}
		})
	}
}

func TestHandleCapabilitiesMessage(t *testing.T) {
	handler := baseHandler{
		done:           internal.NewDone(),
		capabilities:   make(map[string]struct{}),
		capabilitiesCh: make(chan struct{}),
	}

	handler.handleHiddenMessage(".syn capabilities query-update-v1 feature-two")

	if !handler.HasCapability(protocol.CapabilityQueryUpdateV1) {
		t.Fatalf("expected handler to track %q", protocol.CapabilityQueryUpdateV1)
	}
	if !handler.HasCapability("feature-two") {
		t.Fatalf("expected handler to track feature-two")
	}
	if handler.WaitForCapabilities(10*time.Millisecond) != true {
		t.Fatalf("expected capabilities wait to succeed")
	}

	capabilities := handler.Capabilities()
	if len(capabilities) != 2 {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}

func TestWaitForCapabilitiesTimeout(t *testing.T) {
	handler := baseHandler{
		done:           internal.NewDone(),
		capabilities:   make(map[string]struct{}),
		capabilitiesCh: make(chan struct{}),
	}

	if handler.WaitForCapabilities(5 * time.Millisecond) {
		t.Fatalf("expected capabilities wait to time out")
	}
}
