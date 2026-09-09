package user

import (
	"errors"
	osuser "os/user"
	"strings"
	"testing"
)

func TestNameReturnsCurrentUserErrors(t *testing.T) {
	setCurrentUserLookup(t, func() (*osuser.User, error) {
		return nil, errors.New("identity service unavailable")
	})

	_, err := Name()
	if err == nil || !strings.Contains(err.Error(), "identity service unavailable") {
		t.Fatalf("Name error = %v, want wrapped lookup error", err)
	}
}

func TestNameRejectsEmptyUserName(t *testing.T) {
	setCurrentUserLookup(t, func() (*osuser.User, error) {
		return &osuser.User{}, nil
	})

	if _, err := Name(); err == nil || !strings.Contains(err.Error(), "empty user name") {
		t.Fatalf("Name error = %v, want empty user name", err)
	}
}

func TestNoRootCheckReturnsRootIdentityErrors(t *testing.T) {
	tests := []struct {
		name string
		user *osuser.User
		want string
	}{
		{name: "UID", user: &osuser.User{Uid: "0", Gid: "1000"}, want: "UID 0"},
		{name: "GID", user: &osuser.User{Uid: "1000", Gid: "0"}, want: "GID 0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setCurrentUserLookup(t, func() (*osuser.User, error) { return test.user, nil })
			if err := NoRootCheck(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NoRootCheck error = %v, want %q", err, test.want)
			}
		})
	}
}

func setCurrentUserLookup(t *testing.T, lookup func() (*osuser.User, error)) {
	t.Helper()
	original := current
	current = lookup
	t.Cleanup(func() { current = original })
}
