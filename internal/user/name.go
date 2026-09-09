package user

import (
	"fmt"
	"os/user"
)

var current = user.Current

// NoRootCheck verifies that the DTail run user is not with UID or GID 0.
func NoRootCheck() error {
	currentUser, err := current()
	if err != nil {
		return fmt.Errorf("look up current user: %w", err)
	}
	if currentUser == nil {
		return fmt.Errorf("look up current user: no user returned")
	}
	if currentUser.Uid == "0" {
		return fmt.Errorf("not allowed to run as UID 0")
	}
	if currentUser.Gid == "0" {
		return fmt.Errorf("not allowed to run as GID 0")
	}
	return nil
}

// Name of the current run user.
func Name() (string, error) {
	currentUser, err := current()
	if err != nil {
		return "", fmt.Errorf("look up current user: %w", err)
	}
	if currentUser == nil || currentUser.Username == "" {
		return "", fmt.Errorf("look up current user: empty user name")
	}
	return currentUser.Username, nil
}
