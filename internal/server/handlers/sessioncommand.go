package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/session"
)

const (
	sessionAckStartOKPrefix  = ".syn session start ok"
	sessionAckUpdateOKPrefix = ".syn session update ok"
	sessionAckErrorPrefix    = ".syn session err "
)

type sessionCommandState struct {
	mu         sync.Mutex
	active     bool
	generation uint64
	spec       session.Spec
}

func (h *ServerHandler) handleSessionCommand(_ context.Context, _ lcontext.LContext, argc int, args []string, commandFinished func()) {
	defer commandFinished()

	action, generation, spec, err := parseSessionCommand(args, argc)
	if err != nil {
		h.send(h.serverMessages, sessionAckErrorPrefix+err.Error())
		return
	}

	switch action {
	case "START":
		h.sessionState.storeStart(spec)
		h.send(h.serverMessages, sessionAckStartOKPrefix)
	case "UPDATE":
		if !h.sessionState.activeSession() {
			h.send(h.serverMessages, sessionAckErrorPrefix+"session not started")
			return
		}
		h.sessionState.storeUpdate(spec, generation)
		h.send(h.serverMessages, sessionAckUpdateOKPrefix)
	default:
		h.send(h.serverMessages, sessionAckErrorPrefix+"unknown action")
	}
}

func parseSessionCommand(args []string, argc int) (action string, generation uint64, spec session.Spec, err error) {
	if argc < 3 {
		return "", 0, spec, fmt.Errorf("invalid SESSION command")
	}

	action = strings.ToUpper(strings.TrimSpace(args[1]))
	payloadIndex := 2
	if action == "UPDATE" && argc >= 4 {
		generation, err = strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return "", 0, spec, fmt.Errorf("invalid session generation")
		}
		payloadIndex = 3
	}

	payload, err := base64.StdEncoding.DecodeString(args[payloadIndex])
	if err != nil {
		return "", 0, spec, fmt.Errorf("invalid session payload")
	}
	if err := json.Unmarshal(payload, &spec); err != nil {
		return "", 0, spec, fmt.Errorf("invalid session spec")
	}
	if err := validateSessionSpec(spec); err != nil {
		return "", 0, spec, err
	}

	return action, generation, spec, nil
}

func validateSessionSpec(spec session.Spec) error {
	switch spec.Mode {
	case omode.TailClient, omode.CatClient, omode.GrepClient, omode.MapClient, omode.HealthClient:
	default:
		return fmt.Errorf("unsupported session mode")
	}

	if spec.Query != "" && spec.Mode != omode.MapClient && spec.Mode != omode.TailClient {
		return fmt.Errorf("query sessions require map or tail mode")
	}

	if spec.Query == "" && spec.Mode == omode.MapClient {
		return fmt.Errorf("missing session query")
	}

	if _, err := spec.Commands(); err != nil {
		return fmt.Errorf("invalid session spec")
	}

	return nil
}

func (s *sessionCommandState) storeStart(spec session.Spec) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.active = true
	s.generation = 1
	s.spec = spec
}

func (s *sessionCommandState) storeUpdate(spec session.Spec, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.active = true
	if generation == 0 {
		generation = s.generation + 1
	}
	s.generation = generation
	s.spec = spec
}

func (s *sessionCommandState) activeSession() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *sessionCommandState) advertisedCapabilities() string {
	return protocol.HiddenCapabilitiesPrefix + protocol.CapabilityQueryUpdateV1
}
