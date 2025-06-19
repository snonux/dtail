package errors

import (
	"errors"
	"fmt"
)

// Sentinel errors for common error conditions
var (
	// Connection errors
	ErrConnectionFailed    = errors.New("connection failed")
	ErrConnectionTimeout   = errors.New("connection timeout")
	ErrConnectionRefused   = errors.New("connection refused")
	ErrTooManyConnections  = errors.New("too many connections")
	
	// Authentication/Permission errors
	ErrPermissionDenied    = errors.New("permission denied")
	ErrAuthenticationFailed = errors.New("authentication failed")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrInvalidCredentials  = errors.New("invalid credentials")
	
	// Configuration errors
	ErrInvalidConfig       = errors.New("invalid configuration")
	ErrMissingConfig       = errors.New("missing configuration")
	ErrConfigValidation    = errors.New("configuration validation failed")
	
	// File/IO errors
	ErrFileNotFound        = errors.New("file not found")
	ErrFileAccessDenied    = errors.New("file access denied")
	ErrInvalidPath         = errors.New("invalid path")
	ErrReadFailed          = errors.New("read failed")
	ErrWriteFailed         = errors.New("write failed")
	
	// Protocol errors
	ErrInvalidProtocol     = errors.New("invalid protocol")
	ErrProtocolMismatch    = errors.New("protocol version mismatch")
	ErrInvalidCommand      = errors.New("invalid command")
	ErrInvalidQuery        = errors.New("invalid query")
	
	// Resource errors
	ErrResourceExhausted   = errors.New("resource exhausted")
	ErrBufferFull          = errors.New("buffer full")
	ErrTimeout             = errors.New("operation timeout")
	
	// General errors
	ErrInvalidArgument     = errors.New("invalid argument")
	ErrNotImplemented      = errors.New("not implemented")
	ErrInternal            = errors.New("internal error")
)

// Error wrapping functions

// Wrap wraps an error with additional context
func Wrap(err error, msg string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// Wrapf wraps an error with formatted context
func Wrapf(err error, format string, args ...interface{}) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}

// New creates a new error with formatted message
func New(format string, args ...interface{}) error {
	return fmt.Errorf(format, args...)
}

// Is checks if an error is of a specific type
func Is(err, target error) bool {
	return errors.Is(err, target)
}

// As attempts to extract a specific error type
func As(err error, target interface{}) bool {
	return errors.As(err, target)
}

// Unwrap returns the wrapped error
func Unwrap(err error) error {
	return errors.Unwrap(err)
}

// Multi-error support for operations that can have multiple failures

// MultiError represents multiple errors
type MultiError struct {
	errors []error
}

// NewMultiError creates a new MultiError
func NewMultiError() *MultiError {
	return &MultiError{
		errors: make([]error, 0),
	}
}

// Add adds an error to the MultiError
func (m *MultiError) Add(err error) {
	if err != nil {
		m.errors = append(m.errors, err)
	}
}

// HasErrors returns true if there are any errors
func (m *MultiError) HasErrors() bool {
	return len(m.errors) > 0
}

// Error implements the error interface
func (m *MultiError) Error() string {
	if len(m.errors) == 0 {
		return ""
	}
	if len(m.errors) == 1 {
		return m.errors[0].Error()
	}
	return fmt.Sprintf("multiple errors occurred: %v", m.errors)
}

// Errors returns all collected errors
func (m *MultiError) Errors() []error {
	return m.errors
}

// ErrorOrNil returns nil if no errors, otherwise returns the MultiError
func (m *MultiError) ErrorOrNil() error {
	if m.HasErrors() {
		return m
	}
	return nil
}