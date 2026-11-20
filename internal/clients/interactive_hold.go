package clients

import (
	"bytes"
	"errors"
	"io"
	"sync"
)

// outputHoldMaxBytes is how much terminal output an outputHold keeps while
// a Tab waits for a server's answer. Beyond it the writers wait as while
// the output is paused, so the memory stays bounded; the answer then may
// not arrive and the Tab gives up with its hint.
const outputHoldMaxBytes = 4 << 20

// outputHold keeps the terminal output of an interactive session in memory
// while the paused output must not block its writers: a Tab (or :fields
// TABLE) waiting for a server's answer, which the connection handlers read
// only while they can write the output (interactivePause.holdOutput). The
// client logger's terminal output (clientlog.Hold) and the serverless
// direct output (outputGate) hand it what they would have written, each
// with the writer the text goes to. When the output resumes, release
// queues all of it for writing, and writeNext (the pause's drain
// goroutine) writes the queue piece by piece, unchanged and in the order
// taken, one queue for every release, so that text held later never
// overtakes text held before; text still queued when the output is paused
// again stays queued. It keeps at most max bytes, held and queued together: once a
// text did not fit, nothing more is taken until the release, so that no
// later text overtakes one whose writer waits. A nil *outputHold takes
// nothing.
type outputHold struct {
	mu      sync.Mutex
	max     int
	size    int
	holding bool
	// full is set once a text was refused for its size.
	full bool
	// held is what was taken since the last release, queued what was
	// released and is not written yet.
	held   []heldChunk
	queued []heldChunk
	// piece is the piece size of writeNext, holdPieceBytes when zero.
	piece int
}

// heldChunk is consecutive held text of one destination.
type heldChunk struct {
	w    io.Writer
	data []byte
}

func newOutputHold(max int) *outputHold {
	return &outputHold{max: max}
}

// start makes the hold take text, stop makes it refuse text again; what it
// took stays until release.
func (h *outputHold) start() { h.setHolding(true) }

func (h *outputHold) stop() { h.setHolding(false) }

func (h *outputHold) setHolding(holding bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.holding = holding
}

// take copies p, to be written to w after the release, and reports whether
// it did: not while the hold is stopped, and not once a text did not fit.
// w must be comparable (a pointer, or a struct of pointers): consecutive
// texts of one writer are kept, and later written, as one.
func (h *outputHold) take(w io.Writer, p []byte) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.holding || h.full {
		return false
	}
	if h.size+len(p) > h.max {
		h.full = true
		return false
	}
	h.size += len(p)
	if last := len(h.held) - 1; last >= 0 && h.held[last].w == w {
		h.held[last].data = append(h.held[last].data, p...)
		return true
	}
	h.held = append(h.held, heldChunk{w: w, data: append([]byte(nil), p...)})
	return true
}

// isFull reports whether the hold refused a text for its size since the
// last release: the writers block then, and no answer of a server arrives
// until the output resumes.
func (h *outputHold) isFull() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.full
}

// release queues the held text for writing, behind what earlier releases
// queued, and reports whether anything is queued.
func (h *outputHold) release() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queued = append(h.queued, h.held...)
	h.held, h.full = nil, false
	return len(h.queued) > 0
}

// hasQueued reports whether released text waits to be written.
func (h *outputHold) hasQueued() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.queued) > 0
}

// holdPieceBytes is about how much queued text one writeNext writes: the
// drain looks between two pieces whether the output was paused again, so a
// pause stops a large queue promptly.
const holdPieceBytes = 4 << 10

// heldStep is what a writeNext did.
type heldStep int

const (
	// heldEmpty: nothing is queued.
	heldEmpty heldStep = iota
	// heldWrote: a piece was written.
	heldWrote
	// heldBusy: the writer can not take the piece now (another pauser has
	// the logger); it stays queued.
	heldBusy
)

// writeNext writes the next piece of the queued text to its writer: about
// holdPieceBytes of the first queued chunk, ending at a line break, so
// that a pause between two pieces tears no line (nextPiece). A writer that
// refuses the piece for now (an error whose Paused method reports true,
// loggers.heldOutput) leaves it queued. The writer is called without the
// hold's lock, which the output paths take under their own, and may block
// (a stalled terminal): only the drain goroutine calls it.
func (h *outputHold) writeNext() heldStep {
	if h == nil {
		return heldEmpty
	}
	h.mu.Lock()
	if len(h.queued) == 0 {
		h.mu.Unlock()
		return heldEmpty
	}
	w := h.queued[0].w
	piece := nextPiece(h.queued[0].data, h.pieceBytes())
	h.mu.Unlock()
	n, err := w.Write(piece)
	var paused interface{ Paused() bool }
	if n == 0 && errors.As(err, &paused) && paused.Paused() {
		return heldBusy
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Only this goroutine takes text off the queue; releases append.
	h.size -= len(piece)
	if h.queued[0].data = h.queued[0].data[len(piece):]; len(h.queued[0].data) == 0 {
		h.queued = h.queued[1:]
	}
	return heldWrote
}

// pieceBytes returns the piece size: piece, which tests set, else
// holdPieceBytes.
func (h *outputHold) pieceBytes() int {
	if h.piece > 0 {
		return h.piece
	}
	return holdPieceBytes
}

// nextPiece returns the start of data to write next: all of it up to limit
// bytes, else up to its last line break within limit, or, for a line
// longer than limit, up to that line's end.
func nextPiece(data []byte, limit int) []byte {
	if len(data) <= limit {
		return data
	}
	if end := bytes.LastIndexByte(data[:limit], '\n'); end >= 0 {
		return data[:end+1]
	}
	if end := bytes.IndexByte(data[limit:], '\n'); end >= 0 {
		return data[:limit+end+1]
	}
	return data
}
