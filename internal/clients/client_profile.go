package clients

import (
	"github.com/mimecast/dtail/internal/clients/clientlog"
	"github.com/mimecast/dtail/internal/clients/handlers"
	sessionspec "github.com/mimecast/dtail/internal/session"
)

type clientProfile struct {
	newHandler func(server string) handlers.Handler
	retry      bool
	commit     func(spec sessionspec.Spec, generation uint64) error
	// failedCommandStatus is the least exit status of a connection whose
	// server reported a failed command (see sessionStatus); 0 leaves the
	// handler's status as it is, so that dtail's follow mode, dmap (whose
	// scheduled modes handle the failures themselves) and dtailhealth keep
	// their exit statuses.
	failedCommandStatus int
}

func clientHandlerProfile(logger clientlog.Logger, retry bool) clientProfile {
	return clientProfile{
		newHandler: func(server string) handlers.Handler {
			return handlers.NewClientHandler(server, logger)
		},
		retry: retry,
	}
}

// readClientProfile is the profile of the one-shot read clients dcat and
// dgrep: no retry, and exit status ExitStatusFailedCommand when a server
// reported a failed command, e.g. a file it could not read.
func readClientProfile(logger clientlog.Logger) clientProfile {
	profile := clientHandlerProfile(logger, false)
	profile.failedCommandStatus = ExitStatusFailedCommand
	return profile
}
