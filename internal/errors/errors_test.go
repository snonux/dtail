package errors

import (
	"errors"
	"strings"
	"testing"
)

func TestWrap(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		msg      string
		expected string
	}{
		{
			name:     "wrap with message",
			err:      ErrFileNotFound,
			msg:      "opening config file",
			expected: "opening config file: file not found",
		},
		{
			name:     "wrap nil error",
			err:      nil,
			msg:      "should return nil",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Wrap(tt.err, tt.msg)
			if tt.err == nil && result != nil {
				t.Errorf("expected nil, got %v", result)
			}
			if tt.err != nil && result.Error() != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result.Error())
			}
		})
	}
}

func TestWrapf(t *testing.T) {
	err := Wrapf(ErrConnectionFailed, "connecting to %s:%d", "localhost", 2222)
	expected := "connecting to localhost:2222: connection failed"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestIs(t *testing.T) {
	wrapped := Wrap(ErrPermissionDenied, "accessing /etc/passwd")
	
	if !Is(wrapped, ErrPermissionDenied) {
		t.Error("expected Is to return true for wrapped error")
	}
	
	if Is(wrapped, ErrFileNotFound) {
		t.Error("expected Is to return false for different error")
	}
}

func TestMultiError(t *testing.T) {
	multi := NewMultiError()
	
	// Test empty multi-error
	if multi.HasErrors() {
		t.Error("new MultiError should not have errors")
	}
	if multi.ErrorOrNil() != nil {
		t.Error("ErrorOrNil should return nil for empty MultiError")
	}
	
	// Add errors
	multi.Add(ErrConnectionFailed)
	multi.Add(nil) // Should be ignored
	multi.Add(ErrTimeout)
	
	if !multi.HasErrors() {
		t.Error("MultiError should have errors after adding")
	}
	
	if len(multi.Errors()) != 2 {
		t.Errorf("expected 2 errors, got %d", len(multi.Errors()))
	}
	
	// Test error message
	errMsg := multi.Error()
	if !strings.Contains(errMsg, "multiple errors occurred") {
		t.Errorf("unexpected error message: %s", errMsg)
	}
	
	// Test single error
	single := NewMultiError()
	single.Add(ErrInvalidArgument)
	if single.Error() != "invalid argument" {
		t.Errorf("single error message incorrect: %s", single.Error())
	}
}

func TestErrorUnwrapping(t *testing.T) {
	base := errors.New("base error")
	wrapped := Wrap(base, "context")
	
	unwrapped := Unwrap(wrapped)
	if unwrapped != base {
		t.Error("Unwrap did not return base error")
	}
}