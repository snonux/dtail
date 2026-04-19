package server

import (
	"context"
	"sync"
	"testing"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/source"
)

// ensureTestDeps initialises config and the dlog server logger once per test
// binary run. It is safe to call from multiple parallel tests because it
// guards initialisation with the nil-checks used elsewhere in the test suite
// (see internal/mapr/server/aggregate_test.go).
func ensureTestDeps(t *testing.T) {
	t.Helper()
	if config.Server == nil {
		config.Server = &config.ServerConfig{}
	}
	if config.Common == nil {
		config.Common = &config.CommonConfig{
			// Use "none" logger to suppress output during tests and avoid
			// the factory panic caused by an empty logger name.
			Logger:   "none",
			LogLevel: "error",
		}
	}
	if dlog.Server == nil {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		var wg sync.WaitGroup
		wg.Add(1)
		dlog.Start(ctx, &wg, source.Server)
	}
}

// newTestUser creates a User with the given permission strings for testing.
// It bypasses PermissionLookup and OS-level checks so the unit tests
// focus solely on iteratePaths logic.
func newTestUser(perms []string) *User {
	return &User{
		Name:          "testuser",
		remoteAddress: "127.0.0.1",
		permissions:   perms,
	}
}

// TestIteratePaths_DenyWins verifies that a deny rule (prefix '!') wins over
// any subsequent allow rule for the same path. This is the canonical
// "deny-wins" / "first-matching-deny" semantics expected from an ACL system.
//
// Before the fix, the loop used last-match-wins semantics, so a trailing
// allow rule could silently neutralise an earlier deny — a security footgun.
func TestIteratePaths_DenyWins(t *testing.T) {
	ensureTestDeps(t)
	t.Parallel()

	tests := []struct {
		name        string
		permissions []string
		path        string
		permType    string
		wantPerm    bool
	}{
		{
			// Deny rule comes first; a later allow rule must NOT override it.
			name: "deny_then_allow_same_path_deny_wins",
			permissions: []string{
				"readfiles:!/var/log/secret.*",
				"readfiles:/var/log/.*",
			},
			path:     "/var/log/secret.log",
			permType: "readfiles",
			wantPerm: false,
		},
		{
			// Allow rule comes first, then a deny rule — deny must still win.
			name: "allow_then_deny_same_path_deny_wins",
			permissions: []string{
				"readfiles:/var/log/.*",
				"readfiles:!/var/log/secret.*",
			},
			path:     "/var/log/secret.log",
			permType: "readfiles",
			wantPerm: false,
		},
		{
			// A path that is only matched by the allow rule must be permitted.
			name: "allow_non_denied_path",
			permissions: []string{
				"readfiles:!/var/log/secret.*",
				"readfiles:/var/log/.*",
			},
			path:     "/var/log/app.log",
			permType: "readfiles",
			wantPerm: true,
		},
		{
			// Multiple deny rules — any matching deny must block the path.
			name: "multiple_denies_first_matches",
			permissions: []string{
				"readfiles:!/var/log/secret.*",
				"readfiles:!/var/log/private.*",
				"readfiles:/var/log/.*",
			},
			path:     "/var/log/private.log",
			permType: "readfiles",
			wantPerm: false,
		},
		{
			// Path not matched by any rule must not gain permission.
			name: "no_matching_rule_no_permission",
			permissions: []string{
				"readfiles:/var/log/.*",
			},
			path:     "/etc/passwd",
			permType: "readfiles",
			wantPerm: false,
		},
		{
			// Permission type mismatch: rule for a different type must not apply.
			name: "wrong_permission_type_ignored",
			permissions: []string{
				"writefiles:/var/log/.*",
			},
			path:     "/var/log/app.log",
			permType: "readfiles",
			wantPerm: false,
		},
	}

	for _, tc := range tests {
		tc := tc // capture range var for parallel sub-tests
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			u := newTestUser(tc.permissions)
			got, err := u.iteratePaths(tc.path, tc.permType)
			if err != nil {
				t.Fatalf("iteratePaths returned unexpected error: %v", err)
			}
			if got != tc.wantPerm {
				t.Errorf("iteratePaths(%q, %q) = %v, want %v",
					tc.path, tc.permType, got, tc.wantPerm)
			}
		})
	}
}

// TestIteratePaths_InvalidRegex verifies that a malformed regex in the
// permission list is surfaced as an error rather than silently ignored.
func TestIteratePaths_InvalidRegex(t *testing.T) {
	ensureTestDeps(t)
	t.Parallel()

	u := newTestUser([]string{"readfiles:[invalid"})
	_, err := u.iteratePaths("/var/log/app.log", "readfiles")
	if err == nil {
		t.Error("expected an error for invalid regex, got nil")
	}
}
