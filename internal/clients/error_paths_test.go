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
