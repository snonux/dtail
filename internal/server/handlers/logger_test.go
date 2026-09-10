package handlers

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/logging"
	sshserver "github.com/mimecast/dtail/internal/ssh/server"
	userserver "github.com/mimecast/dtail/internal/user/server"
)

var handlerTestLogger logging.NopLogger

type handlerRecordingLogger struct {
	logging.NopLogger
	events []string
}

func (l *handlerRecordingLogger) Info(args ...any) string {
	message := fmt.Sprint(args...)
	l.events = append(l.events, message)
	return message
}

func (l *handlerRecordingLogger) Debug(args ...any) string {
	message := fmt.Sprint(args...)
	l.events = append(l.events, message)
	return message
}

func TestNewServerHandlerPreservesInjectedRoles(t *testing.T) {
	diagnostics := &handlerRecordingLogger{}
	reader := &handlerRecordingLogger{}
	var output bytes.Buffer
	handler, err := NewServerHandler(
		&userserver.User{Name: "logger-test"},
		make(chan struct{}, 1),
		make(chan struct{}, 1),
		&config.ServerConfig{AuthKeyEnabled: true},
		sshserver.NewAuthKeyStore(time.Hour, 1),
		&output,
		HandlerLoggers{Diagnostics: diagnostics, Reader: reader},
	)
	if err != nil {
		t.Fatalf("NewServerHandler: %v", err)
	}
	if handler.Logger() != diagnostics {
		t.Fatal("diagnostic logger was not preserved")
	}
	if handler.ReaderLogger() != reader {
		t.Fatal("reader logger was not preserved")
	}
	if handler.ServerlessOutput() != &output {
		t.Fatal("serverless output writer was not preserved")
	}
	handler.Logger().Info("diagnostic-event")
	handler.ReaderLogger().Info("reader-event")
	if len(diagnostics.events) != 2 || diagnostics.events[1] != "diagnostic-event" {
		t.Fatalf("diagnostic events = %q, want constructor and diagnostic events", diagnostics.events)
	}
	if len(reader.events) != 1 || reader.events[0] != "reader-event" {
		t.Fatalf("reader events = %q, want only reader event", reader.events)
	}
}

func TestNewHealthHandlerPreservesInjectedLogger(t *testing.T) {
	diagnostics := &handlerRecordingLogger{}
	handler, err := NewHealthHandler(&userserver.User{Name: "health-logger-test"}, diagnostics)
	if err != nil {
		t.Fatalf("NewHealthHandler: %v", err)
	}
	if handler.Logger() != diagnostics {
		t.Fatal("diagnostic logger was not preserved")
	}
	handler.Logger().Info("health-event")
	if len(diagnostics.events) != 2 || diagnostics.events[1] != "health-event" {
		t.Fatalf("health diagnostic events = %q, want constructor and health events", diagnostics.events)
	}
}
