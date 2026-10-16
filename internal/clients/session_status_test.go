package clients

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/protocol"
)

const (
	// capsNew is the capabilities frame of a server reporting failures.
	capsNew = protocol.HiddenCapabilitiesPrefix + protocol.CapabilityQueryUpdateV1 + " " +
		protocol.CapabilityCommandFailureV1
	// capsOld is the capabilities frame of a server reporting no failures.
	capsOld   = protocol.HiddenCapabilitiesPrefix + protocol.CapabilityQueryUpdateV1
	closeSync = ".syn close connection"
)

// TestSessionStatus covers how a connection's exit status follows from its
// handler's status and the failed commands its server reported.
func TestSessionStatus(t *testing.T) {
	t.Parallel()

	noFile := handlers.SessionOutcome{Completed: true, Failures: []string{protocol.CommandFailureNoFile}}
	tests := []struct {
		name    string
		profile int
		handler handlers.Handler
		want    int
	}{
		{name: "read client, no failure", profile: ExitStatusFailedCommand,
			handler: &statusTestHandler{outcome: handlers.SessionOutcome{Completed: true}}, want: 0},
		{name: "read client, file read failure", profile: ExitStatusFailedCommand,
			handler: &statusTestHandler{outcome: noFile}, want: ExitStatusFailedCommand},
		{name: "read client, other failed command", profile: ExitStatusFailedCommand,
			handler: &statusTestHandler{outcome: handlers.SessionOutcome{Completed: true,
				Failures: []string{protocol.CommandFailureOutputTooLarge}}}, want: ExitStatusFailedCommand},
		{name: "read client, connection failure and file read failure", profile: ExitStatusFailedCommand,
			handler: &statusTestHandler{status: 1, outcome: noFile}, want: ExitStatusFailedCommand},
		{name: "read client, higher handler status wins", profile: ExitStatusFailedCommand,
			handler: &statusTestHandler{status: 3, outcome: noFile}, want: 3},
		{name: "read client, connection failure only", profile: ExitStatusFailedCommand,
			handler: &statusTestHandler{status: 1}, want: 1},
		{name: "read client, handler without outcome", profile: ExitStatusFailedCommand,
			handler: &retryTestHandler{}, want: 0},
		{name: "other client, file read failure", handler: &statusTestHandler{outcome: noFile}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &baseClient{profile: clientProfile{failedCommandStatus: tt.profile}}
			if got := client.sessionStatus(tt.handler); got != tt.want {
				t.Fatalf("sessionStatus() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestStartExitStatus runs clients whose connections receive the hidden
// messages a server ends a session with, through real client handlers, and
// checks the client's exit status: the highest of its connections.
func TestStartExitStatus(t *testing.T) {
	t.Parallel()

	noFile := protocol.HiddenCommandFailedPrefix + protocol.CommandFailureNoFile
	tooLarge := protocol.HiddenCommandFailedPrefix + protocol.CommandFailureOutputTooLarge
	ok := messageSession{messages: []string{capsNew, closeSync}}
	readFailure := messageSession{messages: []string{capsNew, noFile, closeSync}}
	connFailure := messageSession{messages: []string{capsNew}, serverError: "connection refused"}
	tests := []struct {
		name     string
		profile  clientProfile
		sessions []messageSession
		want     int
	}{
		{name: "dcat, file read", profile: readClientProfile(nil), sessions: []messageSession{ok}, want: 0},
		{name: "dcat, missing file", profile: readClientProfile(nil),
			sessions: []messageSession{readFailure}, want: ExitStatusFailedCommand},
		{name: "dcat, output too large", profile: readClientProfile(nil),
			sessions: []messageSession{{messages: []string{capsNew, tooLarge, closeSync}}},
			want:     ExitStatusFailedCommand},
		{name: "dcat, old server without failure reports", profile: readClientProfile(nil),
			sessions: []messageSession{{messages: []string{capsOld, closeSync}}}, want: 0},
		{name: "dcat, missing file on one of two servers", profile: readClientProfile(nil),
			sessions: []messageSession{ok, readFailure}, want: ExitStatusFailedCommand},
		{name: "dcat, connection failure", profile: readClientProfile(nil),
			sessions: []messageSession{ok, connFailure}, want: 1},
		{name: "dcat, connection failure and missing file", profile: readClientProfile(nil),
			sessions: []messageSession{connFailure, readFailure}, want: ExitStatusFailedCommand},
		// dtail's profile without its retry, which would reconnect forever.
		{name: "dtail, missing file", profile: clientHandlerProfile(nil, false),
			sessions: []messageSession{readFailure}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &baseClient{mu: newBaseClientMu(), profile: tt.profile,
				stats: newTailStats(len(tt.sessions), nil, 0, nil)}
			for i, session := range tt.sessions {
				handler := tt.profile.newHandler(fmt.Sprintf("srv%d", i+1))
				client.connections = append(client.connections, &messageConnector{
					retryTestConnector: retryTestConnector{handler: handler}, t: t, session: session})
			}
			if got := client.Start(context.Background(), make(chan string)); got != tt.want {
				t.Fatalf("Start() status = %d, want %d", got, tt.want)
			}
		})
	}
}

// messageSession is what a messageConnector's session receives: the hidden
// messages of the server, then serverError as a connection error unless it
// is empty.
type messageSession struct {
	messages    []string
	serverError string
}

// messageConnector feeds its handler the messages of its session.
type messageConnector struct {
	retryTestConnector
	t       *testing.T
	session messageSession
}

var _ connectors.Connector = (*messageConnector)(nil)

func (c *messageConnector) Start(_ context.Context, cancel context.CancelFunc, _, _ chan struct{}) {
	defer cancel()
	handler := c.Handler()
	// Take the close acknowledgement the handler sends.
	go func() { _, _ = io.Copy(io.Discard, handler) }()
	defer handler.Shutdown()
	for _, message := range c.session.messages {
		if _, err := handler.Write(append([]byte(message), protocol.MessageDelimiter)); err != nil {
			c.t.Errorf("Write(%q) error = %v", message, err)
		}
	}
	if c.session.serverError != "" {
		handler.ReportServerError(c.session.serverError)
	}
}
