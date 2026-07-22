package client

import (
	"context"
	"net"

	"golang.org/x/crypto/ssh"
)

// CustomCallback is a custom host key callback wrapper.
type CustomCallback struct{}

// NewCustomCallback returns a new wrapper.
func NewCustomCallback() (*CustomCallback, error) {
	h := CustomCallback{}
	return &h, nil
}

// Wrap the host key callback. ctx is accepted for interface compatibility
// but the custom callback never blocks so it has nothing to abort.
func (h *CustomCallback) Wrap(_ context.Context) ssh.HostKeyCallback {
	return func(server string, remote net.Addr, key ssh.PublicKey) error {
		return nil
	}
}
