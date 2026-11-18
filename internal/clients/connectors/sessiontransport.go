package connectors

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	sessionspec "github.com/mimecast/dtail/internal/session"
)

var (
	// ErrSessionAckTimeout indicates that no hidden SESSION acknowledgement arrived in time.
	ErrSessionAckTimeout = errors.New("timed out waiting for session acknowledgement")
	// ErrSessionRejected indicates that the server explicitly rejected a SESSION request.
	ErrSessionRejected = errors.New("session request rejected")
	// ErrUnexpectedSessionAck indicates that the client received a malformed or mismatched acknowledgement.
	ErrUnexpectedSessionAck = errors.New("unexpected session acknowledgement")
	// ErrJournalUnsupported indicates that the remote side did not advertise
	// journal support (protocol.CapabilityJournalV1).
	ErrJournalUnsupported = errors.New(journalUnsupportedMessage)
)

// journalUnsupportedMessage is what the user is told when a spec reads a
// journal target from a server without journal support.
const journalUnsupportedMessage = "journal file targets require server capability " + protocol.CapabilityJournalV1

const defaultSessionAckTimeout = 2 * time.Second

// committedSessionState is the session state of one connection: the spec
// and generation the server acknowledged last, and what its initial SESSION
// START sends. applyMu serializes the session transitions (the initial
// start, reloads, rollbacks and replacing the start spec); mu guards the
// committed fields for readers.
type committedSessionState struct {
	applyMu    sync.Mutex
	mu         sync.RWMutex
	committed  bool
	generation uint64
	spec       sessionspec.Spec
	// started is set, under applyMu, once the initial SESSION START began,
	// whether it succeeded or not; the start spec is no longer replaced
	// then.
	started bool
	// startSpec, when hasStartSpec is set, replaces the connector's initial
	// spec in the initial SESSION START (see replaceStartSpec).
	startSpec    sessionspec.Spec
	hasStartSpec bool
}

// replaceStartSpec makes spec the workload of the initial SESSION START if
// that has not begun yet, and reports true then. It reports false once it
// began, whether it succeeded or not: the caller then has to update a
// committed session, as a :reload does. A SESSION START in flight holds
// applyMu, so this waits for its outcome instead of racing with it.
func (s *committedSessionState) replaceStartSpec(spec sessionspec.Spec) bool {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	if s.started {
		return false
	}
	s.startSpec = spec
	s.hasStartSpec = true
	return true
}

func (s *committedSessionState) commit(spec sessionspec.Spec, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.committed = true
	s.generation = generation
	s.spec = spec
}

func (s *committedSessionState) restore(spec sessionspec.Spec, generation uint64, committed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !committed {
		s.committed = false
		s.generation = 0
		s.spec = sessionspec.Spec{}
		return
	}

	s.committed = true
	s.generation = generation
	s.spec = spec
}

func (s *committedSessionState) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.committed = false
	s.generation = 0
	s.spec = sessionspec.Spec{}
}

func (s *committedSessionState) snapshot() (sessionspec.Spec, uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.spec, s.generation, s.committed
}

// dispatchInitialCommands starts the session's workload: the plain command
// stream of a non-interactive client, or SESSION START for an
// --interactive-query client with a known mode. Every protocol 5 server
// supports SESSION START, so a failed start fails the connection; there is
// no fallback to the command stream.
func dispatchInitialCommands(server string, handler handlers.Handler, commands []string,
	interactiveQuery bool, initialSpec sessionspec.Spec, state *committedSessionState,
	logger logging.Logger) error {
	if !interactiveQuery || initialSpec.Mode == omode.Unknown {
		if err := requireJournalCapability(server, handler, initialSpec, defaultCapabilityWait, true); err != nil {
			return err
		}
		return sendCommandStream(handler, commands)
	}

	if err := startInitialSession(server, handler, state, initialSpec, logger); err != nil {
		state.clear()
		return err
	}
	return nil
}

// startInitialSession sends the initial SESSION START of an interactive
// connection: with the spec a :reload set while the connection was still
// connecting (replaceStartSpec), or else initialSpec, the spec the
// connection was created with. The start is marked as begun and its spec
// read under applyMu, which it keeps holding, so a concurrent reload either
// replaces the spec before the start or finds the start begun and updates
// the session after it.
func startInitialSession(server string, handler handlers.Handler, state *committedSessionState,
	initialSpec sessionspec.Spec, logger logging.Logger) error {
	state.applyMu.Lock()
	defer state.applyMu.Unlock()

	state.started = true
	spec := initialSpec
	if state.hasStartSpec {
		spec = state.startSpec
	}
	return applySessionSpecLocked(server, handler, state, spec, 0, defaultSessionAckTimeout, logger)
}

func applySessionSpec(server string, handler handlers.Handler,
	state *committedSessionState, spec sessionspec.Spec, timeout time.Duration,
	logger logging.Logger) error {
	return applySessionSpecWithGeneration(server, handler, state, spec, 0, true, timeout, logger)
}

func applySessionSpecWithGeneration(server string, handler handlers.Handler,
	state *committedSessionState, spec sessionspec.Spec, generation uint64, useCurrentGeneration bool,
	timeout time.Duration, logger logging.Logger) error {
	// Serialize session transitions so an interactive reload cannot race the
	// initial SESSION START bootstrap on the same connection.
	state.applyMu.Lock()
	defer state.applyMu.Unlock()

	if useCurrentGeneration {
		_, generation, _ = state.snapshot()
	}
	return applySessionSpecLocked(server, handler, state, spec, generation, timeout, logger)
}

// applySessionSpecLocked sends SESSION START (generation 0) or SESSION
// UPDATE generation+1 for spec, and commits spec once the server
// acknowledged it. The caller holds state.applyMu.
func applySessionSpecLocked(server string, handler handlers.Handler,
	state *committedSessionState, spec sessionspec.Spec, generation uint64,
	timeout time.Duration, logger logging.Logger) error {
	logger = logging.OrNop(logger)

	// Only a failed SESSION START (generation 0) is reported to the user and
	// sets the exit status: it fails the initial workload. A rejected UPDATE
	// is answered on the interactive TTY and the client keeps running.
	if err := requireJournalCapability(server, handler, spec, defaultCapabilityWait, generation == 0); err != nil {
		return err
	}

	action := "start"
	nextGeneration := uint64(0)
	command, err := spec.StartCommand()
	if err != nil {
		return err
	}

	if generation != 0 {
		action = "update"
		nextGeneration = generation + 1
		command, err = spec.UpdateCommand(nextGeneration)
		if err != nil {
			return err
		}
	}

	drainSessionAcks(handler)
	if sendErr := handler.SendMessage(command); sendErr != nil {
		return sendErr
	}

	ack, ok := handler.WaitForSessionAck(resolveSessionAckTimeout(timeout))
	if !ok {
		return ErrSessionAckTimeout
	}
	if ack.Error != "" {
		return fmt.Errorf("%w: %s", ErrSessionRejected, ack.Error)
	}
	if ack.Action != action {
		return fmt.Errorf("%w: got action %q want %q", ErrUnexpectedSessionAck, ack.Action, action)
	}
	if ack.Generation == 0 {
		return fmt.Errorf("%w: missing generation", ErrUnexpectedSessionAck)
	}
	if action == "update" && ack.Generation != nextGeneration {
		return fmt.Errorf("%w: got generation %d want %d", ErrUnexpectedSessionAck, ack.Generation, nextGeneration)
	}

	state.commit(spec, ack.Generation)
	logger.Debug(server, "Committed session spec", "action", action, "generation", ack.Generation)
	return nil
}

// missingJournalCapability reports whether spec reads a journal target and
// the server of handler, waited for up to timeout, does not advertise
// protocol.CapabilityJournalV1. A nil handler advertises nothing. Journal
// support is the only capability a read target can need: it is a runtime
// property of the server (Linux with journalctl), while everything else a
// spec can ask for, e.g. a path that has to be encoded
// (protocol.EncodeReadPath), every protocol 5 server supports.
func missingJournalCapability(handler handlers.Handler, spec sessionspec.Spec,
	timeout time.Duration) bool {
	if !spec.HasJournalFiles() {
		return false
	}
	if handler == nil {
		return true
	}
	if timeout <= 0 {
		timeout = defaultCapabilityWait
	}
	return !handler.WaitForCapabilities(timeout) || !handler.HasCapability(protocol.CapabilityJournalV1)
}

// requireJournalCapability checks, before any read command of spec is sent
// (plain commands, SESSION START or a :reload's SESSION UPDATE), that the
// server supports journal targets if spec reads one, and returns
// ErrJournalUnsupported wrapped with the server name otherwise. With report
// it also reports journalUnsupportedMessage to the user, which sets a
// non-zero exit status: the initial workload must fail the client, while a
// rejected :reload is answered on the TTY and leaves the running workload,
// and the exit status, as they were.
func requireJournalCapability(server string, handler handlers.Handler, spec sessionspec.Spec,
	timeout time.Duration, report bool) error {
	if !missingJournalCapability(handler, spec, timeout) {
		return nil
	}
	if report && handler != nil {
		handler.ReportServerError(journalUnsupportedMessage)
	}
	return fmt.Errorf("%w: %s", ErrJournalUnsupported, server)
}

// CheckJournalCapability returns ErrJournalUnsupported if spec reads a
// journal target and the server of conn, waited for up to timeout, does not
// support journal targets, and nil otherwise. It reports nothing to the
// user. An interactive :reload checks every connection with it before it
// updates any, so that a server without journal support rejects the reload
// as a whole instead of after other servers already switched to the new
// workload.
func CheckJournalCapability(conn Connector, spec sessionspec.Spec, timeout time.Duration) error {
	if missingJournalCapability(conn.Handler(), spec, timeout) {
		return ErrJournalUnsupported
	}
	return nil
}

// sendCommandStream sends commands, the plain command stream of a
// non-interactive client (session.Spec.Commands), one command frame each. A
// stream holding a read command is enclosed in an input batch
// (protocol.InputBatchBeginCommand ... InputBatchCompleteCommand), so that
// dserver neither ends the session nor finishes a MapReduce aggregate's
// input before every command of the stream was admitted.
func sendCommandStream(handler handlers.Handler, commands []string) error {
	batched := containsReadCommand(commands)
	if batched {
		if err := handler.SendMessage(protocol.InputBatchBeginCommand); err != nil {
			return err
		}
	}
	for _, command := range commands {
		if err := handler.SendMessage(command); err != nil {
			return err
		}
	}
	if batched {
		if err := handler.SendMessage(protocol.InputBatchCompleteCommand); err != nil {
			return err
		}
	}
	return nil
}

// containsReadCommand reports whether commands hold a cat, grep or tail read
// command, also in the "timeout N <mode> <path> <regex>" form. It looks at
// the command name only, which comes before the path, and the path of a read
// command is a single word anyway (protocol.EncodeReadPath encodes a path
// containing a space).
func containsReadCommand(commands []string) bool {
	for _, command := range commands {
		fields := strings.Fields(command)
		if len(fields) == 0 {
			continue
		}
		commandIndex := 0
		if fields[0] == "timeout" && len(fields) >= 3 {
			commandIndex = 2
		}
		commandName := strings.SplitN(fields[commandIndex], ":", 2)[0]
		switch commandName {
		case "cat", "grep", "tail":
			return true
		}
	}
	return false
}

func drainSessionAcks(handler handlers.Handler) {
	for {
		if _, ok := handler.WaitForSessionAck(0); !ok {
			return
		}
	}
}

func resolveSessionAckTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultSessionAckTimeout
	}
	return timeout
}
