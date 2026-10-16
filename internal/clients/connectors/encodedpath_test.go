package connectors

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	sessionspec "github.com/mimecast/dtail/internal/session"
)

const spacedTestPath = "/var/log/my app/a.log"

func TestRequireEncodedReadPathCapability(t *testing.T) {
	tests := []struct {
		name                string
		files               []string
		waitForCapabilities bool
		capabilities        map[string]bool
		wantErr             error
	}{
		{
			name: "spaced path with capability", files: []string{spacedTestPath},
			waitForCapabilities: true,
			capabilities:        map[string]bool{protocol.CapabilityEncodedReadPathV1: true},
		},
		{
			name: "spaced path without capability", files: []string{"/var/log/app.log", spacedTestPath},
			waitForCapabilities: true,
			capabilities:        map[string]bool{protocol.CapabilityQueryUpdateV1: true},
			wantErr:             ErrEncodedReadPathUnsupported,
		},
		{
			name: "prefixed path without capability", files: []string{protocol.EncodedReadPathPrefix + "a.log"},
			waitForCapabilities: true,
			capabilities:        map[string]bool{protocol.CapabilityQueryUpdateV1: true},
			wantErr:             ErrEncodedReadPathUnsupported,
		},
		{
			name: "spaced path without capabilities advertisement", files: []string{spacedTestPath},
			wantErr: ErrEncodedReadPathUnsupported,
		},
		{
			name: "space-free paths need no capability", files: []string{"/var/log/app.log", "/var/log/*.log"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := &mockHandler{waitForCapabilities: tc.waitForCapabilities, capabilities: tc.capabilities}
			spec := sessionspec.Spec{Mode: omode.CatClient, Files: tc.files}
			err := requireSpecCapabilities("srv1", handler, spec, 10*time.Millisecond)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("requireSpecCapabilities() error = %v, want %v", err, tc.wantErr)
			}
			if wantReport := tc.wantErr != nil; (handler.serverError != "") != wantReport {
				t.Fatalf("server error %q, want reported = %v", handler.serverError, wantReport)
			}
			if tc.wantErr != nil && !strings.Contains(handler.serverError,
				"file paths containing spaces require server capability "+protocol.CapabilityEncodedReadPathV1) {
				t.Fatalf("server error %q does not explain the missing capability", handler.serverError)
			}
		})
	}
}

// TestDispatchInitialCommandsRejectsSpacedPathWithoutCapability checks both
// bootstraps, the legacy command stream and SESSION START: a server without
// the capability gets no read command, and the client fails with status 1.
func TestDispatchInitialCommandsRejectsSpacedPathWithoutCapability(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		handler := &mockHandler{
			waitForCapabilities: true,
			capabilities:        map[string]bool{protocol.CapabilityQueryUpdateV1: true},
		}
		spec := sessionspec.Spec{Mode: omode.TailClient, Files: []string{spacedTestPath}}
		commands, err := spec.Commands()
		if err != nil {
			t.Fatal(err)
		}

		err = dispatchInitialCommands("srv1", handler, commands, interactive,
			spec, &committedSessionState{}, logging.NopLogger{})
		if !errors.Is(err, ErrEncodedReadPathUnsupported) {
			t.Fatalf("interactive=%v: expected ErrEncodedReadPathUnsupported, got %v", interactive, err)
		}
		if len(handler.commands) != 0 {
			t.Fatalf("interactive=%v: expected no commands to be sent, got %#v", interactive, handler.commands)
		}
		if handler.Status() != 1 {
			t.Fatalf("interactive=%v: handler status = %d, want 1", interactive, handler.Status())
		}
	}
}

func TestDispatchInitialCommandsSendsSpacedPathWithCapability(t *testing.T) {
	handler := &mockHandler{
		waitForCapabilities: true,
		capabilities:        map[string]bool{protocol.CapabilityEncodedReadPathV1: true},
	}
	spec := sessionspec.Spec{Mode: omode.CatClient, Files: []string{spacedTestPath}}
	commands, err := spec.Commands()
	if err != nil {
		t.Fatal(err)
	}

	if err := dispatchInitialCommands("srv1", handler, commands, false,
		spec, &committedSessionState{}, logging.NopLogger{}); err != nil {
		t.Fatalf("dispatchInitialCommands() error = %v", err)
	}
	want := []string{protocol.InputBatchBeginCommand, commands[0], protocol.InputBatchCompleteCommand}
	if strings.Join(handler.commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sent commands %#v, want %#v", handler.commands, want)
	}
}

// TestApplySessionSpecRejectsSpacedPathReloadWithoutCapability covers a
// :reload --files with a spaced path against a server that supports reloads
// but not encoded paths: the reload is rejected before SESSION UPDATE is sent
// and the committed workload stays.
func TestApplySessionSpecRejectsSpacedPathReloadWithoutCapability(t *testing.T) {
	mock := &mockHandler{
		waitForCapabilities: true,
		capabilities:        map[string]bool{protocol.CapabilityQueryUpdateV1: true},
		sessionAcks:         []handlers.SessionAck{{Action: "start", Generation: 1}},
	}
	conn := &ServerConnection{server: "srv1", handler: mock}
	startSpec := sessionspec.Spec{Mode: omode.TailClient, Files: []string{"/var/log/app.log"}}
	if err := conn.ApplySessionSpec(startSpec, 10*time.Millisecond); err != nil {
		t.Fatalf("start ApplySessionSpec() error = %v", err)
	}
	sent := len(mock.commands)

	err := conn.ApplySessionSpec(sessionspec.Spec{Mode: omode.TailClient, Files: []string{spacedTestPath}},
		10*time.Millisecond)
	if !errors.Is(err, ErrEncodedReadPathUnsupported) {
		t.Fatalf("expected ErrEncodedReadPathUnsupported, got %v", err)
	}
	if len(mock.commands) != sent {
		t.Fatalf("reload sent commands %#v", mock.commands[sent:])
	}
	if committed, generation, ok := conn.CommittedSession(); !ok || generation != 1 ||
		committed.Files[0] != "/var/log/app.log" {
		t.Fatalf("committed session changed: spec=%#v generation=%d ok=%v", committed, generation, ok)
	}
}
