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
		sessionAcks:    make(chan SessionAck, 1),
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
		sessionAcks:    make(chan SessionAck, 1),
	}

	if handler.WaitForCapabilities(5 * time.Millisecond) {
		t.Fatalf("expected capabilities wait to time out")
	}
}

func TestParseSessionAckMessage(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    SessionAck
		wantOK  bool
	}{
		{
			name:    "start ok",
			message: ".syn session start ok 7",
			want: SessionAck{
				Action:     "start",
				Generation: 7,
			},
			wantOK: true,
		},
		{
			name:    "update ok",
			message: ".syn session update ok 8",
			want: SessionAck{
				Action:     "update",
				Generation: 8,
			},
			wantOK: true,
		},
		{
			name:    "error",
			message: ".syn session err query sessions not supported yet",
			want: SessionAck{
				Action: "error",
				Error:  "query sessions not supported yet",
			},
			wantOK: true,
		},
		{
			name:    "invalid",
			message: ".syn session start ok nope",
			wantOK:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseSessionAckMessage(tc.message)
			if ok != tc.wantOK {
				t.Fatalf("unexpected ok flag: got %v want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got != tc.want {
				t.Fatalf("unexpected ack: got %#v want %#v", got, tc.want)
			}
		})
	}
}

func TestHandleSessionAckMessage(t *testing.T) {
	handler := baseHandler{
		done:        internal.NewDone(),
		sessionAcks: make(chan SessionAck, 1),
	}

	handler.handleHiddenMessage(".syn session update ok 4")

	ack, ok := handler.WaitForSessionAck(10 * time.Millisecond)
	if !ok {
		t.Fatalf("expected session ack")
	}
	if ack.Action != "update" || ack.Generation != 4 {
		t.Fatalf("unexpected session ack: %#v", ack)
	}
}
