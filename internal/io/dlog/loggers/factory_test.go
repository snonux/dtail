package loggers

import (
	"strings"
	"testing"
)

func TestFactoryReturnsErrorForUnsupportedLogger(t *testing.T) {
	_, err := Factory("test", "carrier-pigeon", Strategy{})
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("Factory error = %v, want unsupported logger name", err)
	}
}
