package clients

import (
	"errors"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
)

func TestNewGrepClientReturnsInvalidRegexError(t *testing.T) {
	_, err := NewGrepClient(config.Args{
		ConnectionsPerCPU: 1,
		RegexStr:          "[",
		Serverless:        true,
	}, LoggerDependencies{})
	if err == nil || !strings.Contains(err.Error(), "compile regular expression") {
		t.Fatalf("NewGrepClient error = %v, want regex compilation error", err)
	}
}

func TestNewMaprClientReturnsInvalidQueryError(t *testing.T) {
	_, err := NewMaprClient(config.Args{QueryStr: "select from"}, DefaultMode, LoggerDependencies{})
	if err == nil || !strings.Contains(err.Error(), "parse mapreduce query") {
		t.Fatalf("NewMaprClient error = %v, want query parse error", err)
	}
}

func TestMakeConnectionsReturnsSessionConstructionError(t *testing.T) {
	client := &baseClient{}
	err := client.makeConnections(failingSessionMaker{})
	if err == nil || !strings.Contains(err.Error(), "session construction failed") {
		t.Fatalf("makeConnections error = %v, want wrapped session error", err)
	}
}

func TestInitializeClosesAuthenticationResourcesOnConnectionSetupError(t *testing.T) {
	closer := &recordingCloser{}
	client := &baseClient{
		Args:       config.Args{Serverless: true},
		authCloser: closer,
	}

	err := client.initialize(failingSessionMaker{})
	if err == nil || !strings.Contains(err.Error(), "session construction failed") {
		t.Fatalf("initialize error = %v, want wrapped session error", err)
	}
	if closer.calls != 1 {
		t.Fatalf("authentication closer calls = %d, want 1", closer.calls)
	}
	if client.authCloser != nil {
		t.Fatal("initialize retained a closed authentication resource")
	}
}

type recordingCloser struct{ calls int }

func (c *recordingCloser) Close() error {
	c.calls++
	return nil
}

type failingSessionMaker struct{}

func (failingSessionMaker) makeHandler(string) handlers.Handler { return nil }
func (failingSessionMaker) makeSessionSpec() (SessionSpec, error) {
	return SessionSpec{}, errors.New("session construction failed")
}
