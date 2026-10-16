package clients

import "github.com/mimecast/dtail/internal/clients/handlers"

// ExitStatusFailedCommand is the exit status of dcat and dgrep (grep's error
// status) when a server reported a failed command of their session through
// protocol.HiddenCommandFailedPrefix: a file read failure
// (protocol.IsFileReadFailure, e.g. a missing, not permitted or unreadable
// file), and any other reported reason too (e.g. a read command the server
// could not parse, or output too large for the session output buffer), as
// each means the output is missing data that was asked for. It is the same
// in serverless mode, whose in-process server reports failures the same way.
// Servers without protocol.CapabilityCommandFailureV1 report no failures, so
// with them the status stays the connection status. A connection status of
// 1 (a refused or unreachable server, a failed SSH handshake, an SSH session
// or shell that could not be opened, an --interactive-query SESSION START the
// server rejected or did not acknowledge, commands that could not be sent, a
// journal target the server does not support, a failed serverless
// in-process session: every error ServerConnection.Start and Serverless.Start
// pass to Handler.ReportServerError) stays 1 unless such a report raises it;
// the client exits with the highest status of all its connections (see
// baseClient.runConnections). A session cut short without the server's close
// handshake (e.g. a SIGKILLed server mid-read) currently keeps status 0, as
// ServerConnection.handle returns only the dispatch error and
// SessionOutcome.Completed is not checked here.
const ExitStatusFailedCommand = 2

// sessionOutcomeReporter is a client handler that knows how its session
// ended.
type sessionOutcomeReporter interface {
	Outcome() handlers.SessionOutcome
}

// sessionStatus returns the exit status of a connection whose session ran
// with handler: the handler's status, raised to the profile's
// failedCommandStatus when that is set and the server reported a failed
// command. The reports are counted whether or not the session completed,
// and the higher of the two statuses wins. A session cut short gets no
// status of its own from this (see ExitStatusFailedCommand): only the
// failures it reported before it ended raise its status.
func (c *baseClient) sessionStatus(handler handlers.Handler) int {
	status := handler.Status()
	if c.profile.failedCommandStatus == 0 {
		return status
	}
	reporter, ok := handler.(sessionOutcomeReporter)
	if !ok || len(reporter.Outcome().Failures) == 0 {
		return status
	}
	return max(status, c.profile.failedCommandStatus)
}
