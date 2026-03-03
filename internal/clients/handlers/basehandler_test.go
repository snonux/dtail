package handlers

import (
	"fmt"
	"testing"

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
