package connectors

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/omode"
	sessionspec "github.com/mimecast/dtail/internal/session"
)

var (
	// ErrSessionUnsupported indicates that the remote side did not advertise
	// runtime query update support.
	ErrSessionUnsupported = errors.New("runtime query updates unsupported by server")
	// ErrSessionAckTimeout indicates that no hidden SESSION acknowledgement arrived in time.
	ErrSessionAckTimeout = errors.New("timed out waiting for session acknowledgement")
	// ErrSessionRejected indicates that the server explicitly rejected a SESSION request.
	ErrSessionRejected = errors.New("session request rejected")
	// ErrUnexpectedSessionAck indicates that the client received a malformed or mismatched acknowledgement.
	ErrUnexpectedSessionAck = errors.New("unexpected session acknowledgement")
)

const defaultSessionAckTimeout = 2 * time.Second

type committedSessionState struct {
	applyMu    sync.Mutex
	mu         sync.RWMutex
	committed  bool
	generation uint64
	spec       sessionspec.Spec
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

func dispatchInitialCommands(server string, handler handlers.Handler, commands []string,
	interactiveQuery bool, initialSpec sessionspec.Spec, state *committedSessionState) error {

	if !interactiveQuery || initialSpec.Mode == omode.Unknown {
		return sendLegacyCommands(handler, commands)
	}

	if err := applySessionSpec(server, handler, state, initialSpec, defaultSessionAckTimeout); err != nil {
		if !errors.Is(err, ErrSessionUnsupported) {
			dlog.Client.Warn(server, "Interactive session bootstrap failed, falling back to legacy commands", err)
		}
		state.clear()
		return sendLegacyCommands(handler, commands)
	}

	return nil
}

func applySessionSpec(server string, handler handlers.Handler,
	state *committedSessionState, spec sessionspec.Spec, timeout time.Duration) error {
	return applySessionSpecWithGeneration(server, handler, state, spec, 0, true, timeout)
}

func applySessionSpecWithGeneration(server string, handler handlers.Handler,
	state *committedSessionState, spec sessionspec.Spec, generation uint64, useCurrentGeneration bool, timeout time.Duration) error {

	// Serialize session transitions so an interactive reload cannot race the
	// initial SESSION START bootstrap on the same connection.
	state.applyMu.Lock()
	defer state.applyMu.Unlock()

	if useCurrentGeneration {
		_, generation, _ = state.snapshot()
	}

	if handler == nil {
		return ErrSessionUnsupported
	}
	if !supportsQueryUpdates(handler, defaultCapabilityWait) {
		return ErrSessionUnsupported
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
	if err := handler.SendMessage(command); err != nil {
		return err
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
	dlog.Client.Debug(server, "Committed session spec", "action", action, "generation", ack.Generation)
	return nil
}

func sendLegacyCommands(handler handlers.Handler, commands []string) error {
	for _, command := range commands {
		if err := handler.SendMessage(command); err != nil {
			return err
		}
	}
	return nil
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
