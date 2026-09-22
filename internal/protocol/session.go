package protocol

const (
	// HiddenSessionStartOKPrefix acknowledges a successful SESSION START request.
	HiddenSessionStartOKPrefix = ".syn session start ok"
	// HiddenSessionUpdateOKPrefix acknowledges a successful SESSION UPDATE request.
	HiddenSessionUpdateOKPrefix = ".syn session update ok"
	// HiddenSessionErrorPrefix reports a rejected SESSION request.
	HiddenSessionErrorPrefix = ".syn session err "
)

// HiddenCommandFailedPrefix reports that a command of the session failed on
// the server, for example a read whose file(s) did not exist or could not be
// opened, so that the session's output is incomplete. It is followed by a
// short, fixed reason naming no path or server-side error detail (those stay
// in the server log). Servers advertising CapabilityCommandFailureV1 send it
// once per failure, before the session's close handshake; clients that do
// not know it ignore it like any other unknown hidden message.
const HiddenCommandFailedPrefix = ".syn command failed "
