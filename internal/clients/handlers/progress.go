package handlers

import "github.com/mimecast/dtail/internal/protocol"

// ProgressReporter is a handler that knows the read progress its server
// reported (protocol.HiddenProgressPrefix). Every client handler built on
// baseHandler is one; it is an interface of its own, asserted where needed,
// so that Handler and its test doubles stay as they are.
type ProgressReporter interface {
	// Progress returns the latest read progress of the session and whether
	// the server reported any yet. A server of before the progress frame
	// never does.
	Progress() (protocol.Progress, bool)
}

// handleProgressMessage keeps the read progress the server reported. Only the
// latest counts, as each report holds the session's totals so far; one that
// does not parse is logged at debug level and dropped.
func (h *baseHandler) handleProgressMessage(message string) {
	progress, err := protocol.ParseProgress(message)
	if err != nil {
		h.log().Debug(h.server, "Ignoring invalid progress message", err)
		return
	}
	h.progress.Store(&progress)
}

// resetProgress forgets the read progress: a SESSION START or UPDATE was
// acknowledged, so what was reported belongs to the replaced workload. The
// server sends the new workload's progress only after its acknowledgement
// (sessionAckGate holds it back) and drops frames of the replaced one, so
// nothing reported for the new workload is lost. Without this a workload
// that reports none (following lines, a journal) kept showing the last
// numbers of the reads before it in the client's statistics.
func (h *baseHandler) resetProgress() {
	h.progress.Store(nil)
}

// Progress returns the latest read progress the server reported, if any.
func (h *baseHandler) Progress() (protocol.Progress, bool) {
	progress := h.progress.Load()
	if progress == nil {
		return protocol.Progress{}, false
	}
	return *progress, true
}
