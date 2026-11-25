package handlers

// The read progress of a session (readProgress) reaches the session output
// through a one-slot mailbox rather than the server message queue:
//
//   - posting never blocks and replaces a frame not taken yet, so only the
//     latest numbers wait and a slow client is not sent stale ones;
//   - the output goroutine takes the mailbox ahead of payload (next), as long
//     as that splits no payload frame. A cat or grep that keeps the session
//     output buffer full would otherwise hold every progress frame back until
//     its read ended, as payload goes before server messages;
//   - it takes it before the close handshake too (tryReadQueued), so the
//     final progress always reaches the client;
//   - a frame of a replaced generation is dropped (readServerMessage), and one
//     of a generation whose SESSION acknowledgement is still held back, or
//     while messages deferred for one wait, stays in the mailbox until they
//     went out (sessionAckGate), so it overtakes none of them.

// postProgress hands the progress frame message (generation-tagged, see
// encodeGeneratedMessage) to the session output, replacing one not sent yet,
// and wakes the output goroutine if it waits.
func (c *outputCoordinator) postProgress(message string) {
	c.progressPending.Store(&message)
	select {
	case c.progressWake <- struct{}{}:
	default:
	}
}

// tryReadProgress takes the posted progress frame, if there is one and it
// may go out now: at a payload frame boundary and with no output held or
// deferred for a SESSION acknowledgement.
func (c *outputCoordinator) tryReadProgress() (outputReadResult, bool) {
	if c.progressPending.Load() == nil || c.ackGate.pending() || !c.output.atRecordBoundary() {
		return outputReadResult{}, false
	}
	message := c.progressPending.Swap(nil)
	if message == nil {
		return outputReadResult{}, false
	}
	return outputReadResult{kind: outputReadServerMessage, message: *message}, true
}

// progressQueued reports whether a progress frame waits in the mailbox.
func (c *outputCoordinator) progressQueued() bool {
	return c.progressPending.Load() != nil
}
