package clients

import (
	"errors"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/clients/handlers"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
)

func TestNewGrepClientReturnsInvalidRegexError(t *testing.T) {
	resetErrorPathLogger(t)

	_, err := NewGrepClient(config.Args{
		ConnectionsPerCPU: 1,
		RegexStr:          "[",
		Serverless:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "compile regular expression") {
		t.Fatalf("NewGrepClient error = %v, want regex compilation error", err)
	}
}

func TestNewMaprClientReturnsInvalidQueryError(t *testing.T) {
	resetErrorPathLogger(t)

	_, err := NewMaprClient(config.Args{QueryStr: "select from"}, DefaultMode)
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
	resetErrorPathLogger(t)
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

func resetErrorPathLogger(t *testing.T) {
	t.Helper()
	original := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() { dlog.Client = original })
}
