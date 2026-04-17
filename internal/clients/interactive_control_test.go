package clients

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
	sessionspec "github.com/mimecast/dtail/internal/session"
)

func TestParseInteractiveCommandForGrepReload(t *testing.T) {
	current := config.Args{
		Mode:     omode.GrepClient,
		What:     "/var/log/app.log",
		RegexStr: "ERROR",
	}

	command, err := parseInteractiveCommand(current,
		`:reload --grep WARN --before 2 --after 3 --max 4 --invert --files "/tmp/other.log" --plain --quiet --timeout 7`)
	if err != nil {
		t.Fatalf("parseInteractiveCommand() error = %v", err)
	}

	if command.kind != "reload" {
		t.Fatalf("command kind = %q, want reload", command.kind)
	}
	if command.next.What != "/tmp/other.log" {
		t.Fatalf("files = %q, want /tmp/other.log", command.next.What)
	}
	if command.next.RegexStr != "WARN" {
		t.Fatalf("regex = %q, want WARN", command.next.RegexStr)
	}
	if !command.next.RegexInvert || !command.next.Plain || !command.next.Quiet {
		t.Fatalf("expected invert/plain/quiet flags to be set: %#v", command.next)
	}
	if command.next.LContext.BeforeContext != 2 || command.next.LContext.AfterContext != 3 || command.next.LContext.MaxCount != 4 {
		t.Fatalf("unexpected context values: %#v", command.next.LContext)
	}
	if command.spec.Regex != "WARN" {
		t.Fatalf("session spec regex = %q, want WARN", command.spec.Regex)
	}
}

func TestParseInteractiveCommandForMapReloadDerivesRegex(t *testing.T) {
	current := config.Args{
		Mode:     omode.MapClient,
		What:     "/var/log/app.log",
		QueryStr: "select count(status) from stats group by status",
	}

	command, err := parseInteractiveCommand(current,
		`:reload --query "select count(status) from warnings group by status" --files /tmp/new.log --plain --timeout 9`)
	if err != nil {
		t.Fatalf("parseInteractiveCommand() error = %v", err)
	}

	if command.kind != "reload" {
		t.Fatalf("command kind = %q, want reload", command.kind)
	}
	if command.next.QueryStr != "select count(status) from warnings group by status" {
		t.Fatalf("query = %q", command.next.QueryStr)
	}
	if command.spec.Regex != "\\|MAPREDUCE:WARNINGS\\|" {
		t.Fatalf("session spec regex = %q, want WARNINGS table regex", command.spec.Regex)
	}
}

func TestParseInteractiveCommandRejectsUnterminatedQuotes(t *testing.T) {
	current := config.Args{
		Mode:     omode.MapClient,
		QueryStr: "select count(status) from stats group by status",
	}

	if _, err := parseInteractiveCommand(current, `:reload --query "select count(status) from stats`); err == nil {
		t.Fatalf("expected parseInteractiveCommand() to reject unterminated quoted input")
	}
}

func TestApplyInteractiveReloadRejectsUnsupportedConnections(t *testing.T) {
	client := &baseClient{
		Args: config.Args{
			Mode:     omode.GrepClient,
			What:     "/var/log/app.log",
			RegexStr: "ERROR",
		},
		sessionSpec: SessionSpec{
			Mode:  omode.GrepClient,
			Files: []string{"/var/log/app.log"},
			Regex: "ERROR",
		},
		connections: []connectors.Connector{
			&interactiveReloadConnector{server: "srv1", supported: false},
		},
	}

	err := client.applyInteractiveReload(config.Args{
		Mode:     omode.GrepClient,
		What:     "/tmp/next.log",
		RegexStr: "WARN",
	}, SessionSpec{
		Mode:  omode.GrepClient,
		Files: []string{"/tmp/next.log"},
		Regex: "WARN",
	})
	if !errors.Is(err, connectors.ErrSessionUnsupported) {
		t.Fatalf("expected ErrSessionUnsupported, got %v", err)
	}
	if client.Args.What != "/var/log/app.log" || client.sessionSpec.Regex != "ERROR" {
		t.Fatalf("client state changed on unsupported reload: args=%#v spec=%#v", client.Args, client.sessionSpec)
	}
}

func TestApplyInteractiveReloadRollsBackPartialFailure(t *testing.T) {
	oldArgs := config.Args{
		Mode:     omode.GrepClient,
		What:     "/var/log/app.log",
		RegexStr: "ERROR",
	}
	oldSpec := SessionSpec{
		Mode:  omode.GrepClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}
	rollbackErr := errors.New("boom")

	connA := &interactiveReloadConnector{server: "srv1", supported: true, appliedSpec: oldSpec, generation: 4}
	connB := &interactiveReloadConnector{server: "srv2", supported: true, appliedSpec: oldSpec, generation: 4}
	connC := &interactiveReloadConnector{server: "srv3", supported: true, appliedSpec: oldSpec, applyErr: rollbackErr, generation: 4}
	maker := &interactiveReloadMaker{}

	client := &baseClient{
		Args:        oldArgs,
		sessionSpec: oldSpec,
		connections: []connectors.Connector{connA, connB, connC},
		maker:       maker,
	}

	nextArgs := config.Args{
		Mode:     omode.GrepClient,
		What:     "/tmp/new.log",
		RegexStr: "WARN",
	}
	nextSpec := SessionSpec{
		Mode:  omode.GrepClient,
		Files: []string{"/tmp/new.log"},
		Regex: "WARN",
	}

	err := client.applyInteractiveReload(nextArgs, nextSpec)
	if err == nil || !errors.Is(err, rollbackErr) {
		t.Fatalf("expected reload error from failing connection, got %v", err)
	}

	if !reflect.DeepEqual(client.Args, oldArgs) || !reflect.DeepEqual(client.sessionSpec, oldSpec) {
		t.Fatalf("client state changed on partial failure: args=%#v spec=%#v", client.Args, client.sessionSpec)
	}
	if len(maker.commits) != 0 {
		t.Fatalf("expected no committed shared state, got %#v", maker.commits)
	}
	if !reflect.DeepEqual(connA.appliedSpec, oldSpec) || !reflect.DeepEqual(connB.appliedSpec, oldSpec) {
		t.Fatalf("expected successful connections to roll back to old spec: %#v %#v", connA.appliedSpec, connB.appliedSpec)
	}
	if connA.generation != 4 || connB.generation != 4 {
		t.Fatalf("expected rollback to preserve original generations, got %d and %d", connA.generation, connB.generation)
	}
	if connA.restoreCount != 1 || connB.restoreCount != 1 || connC.restoreCount != 1 {
		t.Fatalf("unexpected rollback counts: %#v %#v %#v", connA.restoreCount, connB.restoreCount, connC.restoreCount)
	}
	if !reflect.DeepEqual(connC.appliedSpec, oldSpec) || connC.generation != 4 {
		t.Fatalf("failed connection should remain on original committed session: %#v generation=%d", connC.appliedSpec, connC.generation)
	}
}

func TestApplyInteractiveReloadCommitsSharedState(t *testing.T) {
	oldSpec := SessionSpec{
		Mode:  omode.MapClient,
		Files: []string{"/var/log/app.log"},
		Query: "select count(status) from stats group by status",
		Regex: "\\|MAPREDUCE:STATS\\|",
	}
	connA := &interactiveReloadConnector{server: "srv1", supported: true, appliedSpec: oldSpec, generation: 4}
	connB := &interactiveReloadConnector{server: "srv2", supported: true, appliedSpec: oldSpec, generation: 4}
	maker := &interactiveReloadMaker{}

	client := &baseClient{
		Args: config.Args{
			Mode:     omode.MapClient,
			What:     "/var/log/app.log",
			QueryStr: "select count(status) from stats group by status",
		},
		sessionSpec: SessionSpec{
			Mode:  omode.MapClient,
			Files: []string{"/var/log/app.log"},
			Query: "select count(status) from stats group by status",
			Regex: "\\|MAPREDUCE:STATS\\|",
		},
		connections: []connectors.Connector{connA, connB},
		maker:       maker,
	}

	nextArgs := config.Args{
		Mode:     omode.MapClient,
		What:     "/tmp/new.log",
		QueryStr: "select count(status) from warnings group by status",
		Plain:    true,
		Timeout:  5,
	}
	nextSpec := SessionSpec{
		Mode:    omode.MapClient,
		Files:   []string{"/tmp/new.log"},
		Query:   nextArgs.QueryStr,
		Regex:   "\\|MAPREDUCE:WARNINGS\\|",
		Timeout: 5,
	}

	if err := client.applyInteractiveReload(nextArgs, nextSpec); err != nil {
		t.Fatalf("applyInteractiveReload() error = %v", err)
	}

	if client.Args.What != "/tmp/new.log" || client.sessionSpec.Query != nextArgs.QueryStr {
		t.Fatalf("client state not committed: args=%#v spec=%#v", client.Args, client.sessionSpec)
	}
	if len(maker.commits) != 1 {
		t.Fatalf("expected one sessionCommitter call, got %d", len(maker.commits))
	}
	if maker.commits[0].generation != 5 || maker.commits[0].spec.Query != nextArgs.QueryStr {
		t.Fatalf("unexpected commit payload: %#v", maker.commits[0])
	}
	if connA.appliedSpec.Query != nextArgs.QueryStr || connB.appliedSpec.Query != nextArgs.QueryStr {
		t.Fatalf("connectors did not receive new session spec: %#v %#v", connA.appliedSpec, connB.appliedSpec)
	}
}

func TestApplyInteractiveReloadRejectsMismatchedCommittedGenerations(t *testing.T) {
	oldSpec := SessionSpec{
		Mode:  omode.GrepClient,
		Files: []string{"/var/log/app.log"},
		Regex: "ERROR",
	}
	connA := &interactiveReloadConnector{server: "srv1", supported: true, appliedSpec: oldSpec, generation: 4}
	connB := &interactiveReloadConnector{server: "srv2", supported: true, appliedSpec: oldSpec, generation: 5}
	maker := &interactiveReloadMaker{}

	client := &baseClient{
		Args: config.Args{
			Mode:     omode.GrepClient,
			What:     "/var/log/app.log",
			RegexStr: "ERROR",
		},
		sessionSpec: SessionSpec{
			Mode:  omode.GrepClient,
			Files: []string{"/var/log/app.log"},
			Regex: "ERROR",
		},
		connections: []connectors.Connector{connA, connB},
		maker:       maker,
	}

	nextArgs := config.Args{
		Mode:     omode.GrepClient,
		What:     "/tmp/new.log",
		RegexStr: "WARN",
	}
	nextSpec := SessionSpec{
		Mode:  omode.GrepClient,
		Files: []string{"/tmp/new.log"},
		Regex: "WARN",
	}

	err := client.applyInteractiveReload(nextArgs, nextSpec)
	if err == nil || err.Error() != "mismatched committed generations: got 5 and 6" {
		t.Fatalf("expected mismatched generation error, got %v", err)
	}
	if client.Args.What != "/var/log/app.log" || client.sessionSpec.Regex != "ERROR" {
		t.Fatalf("client state changed on mismatched generations: args=%#v spec=%#v", client.Args, client.sessionSpec)
	}
	if len(maker.commits) != 0 {
		t.Fatalf("expected no committed shared state, got %#v", maker.commits)
	}
	if connA.generation != 4 || connB.generation != 5 {
		t.Fatalf("expected rollback to restore original generations, got %d and %d", connA.generation, connB.generation)
	}
	if connA.restoreCount != 1 || connB.restoreCount != 1 {
		t.Fatalf("expected rollback to restore both connections once, got %d and %d", connA.restoreCount, connB.restoreCount)
	}
}

type interactiveReloadConnector struct {
	appliedSpec  sessionspec.Spec
	applyErr     error
	generation   uint64
	server       string
	supported    bool
	restoreCount int
}

func (*interactiveReloadConnector) Start(context.Context, context.CancelFunc, chan struct{}, chan struct{}) {
}

func (c *interactiveReloadConnector) Server() string { return c.server }

func (*interactiveReloadConnector) Handler() handlers.Handler { return nil }

func (c *interactiveReloadConnector) SupportsQueryUpdates(time.Duration) bool { return c.supported }

func (c *interactiveReloadConnector) ApplySessionSpec(spec sessionspec.Spec, _ time.Duration) error {
	if c.applyErr != nil {
		return c.applyErr
	}
	c.generation++
	c.appliedSpec = spec
	return nil
}

func (c *interactiveReloadConnector) CommittedSession() (sessionspec.Spec, uint64, bool) {
	if c.generation == 0 {
		return sessionspec.Spec{}, 0, false
	}
	return c.appliedSpec, c.generation, true
}

func (c *interactiveReloadConnector) RestoreCommittedSession(spec sessionspec.Spec, generation uint64, committed bool) {
	c.restoreCount++
	if !committed {
		c.appliedSpec = sessionspec.Spec{}
		c.generation = 0
		return
	}
	c.appliedSpec = spec
	c.generation = generation
}

type interactiveReloadMaker struct {
	commits []interactiveReloadCommit
}

type interactiveReloadCommit struct {
	generation uint64
	spec       SessionSpec
}

func (*interactiveReloadMaker) makeHandler(string) handlers.Handler { return nil }

func (*interactiveReloadMaker) makeCommands() []string { return nil }

func (m *interactiveReloadMaker) commitSessionSpec(spec SessionSpec, generation uint64) error {
	m.commits = append(m.commits, interactiveReloadCommit{
		generation: generation,
		spec:       spec,
	})
	return nil
}
