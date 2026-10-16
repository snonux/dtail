package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

// TestReadCommandHonoursInvertedMatchAllPatterns runs a grep the two ways a
// command can be split into words before readCommand.Start: at every space,
// which keeps the empty last word of an empty pattern, and at runs of
// whitespace, which drops it. An inverted empty pattern must match no line
// either way rather than turn into a noop. A lone third word is a regex only
// with the "regex:" prefix: flags which do not parse fail the command, any
// other word is ignored and the command matches every line.
func TestReadCommandHonoursInvertedMatchAllPatterns(t *testing.T) {
	const content = "alpha\n\nbeta\n"
	sep := string([]byte{protocol.MessageDelimiter})
	all := "alpha\n" + sep + "\n" + sep + "beta\n" + sep

	tests := []struct {
		name        string
		regex       string
		split       func(string) []string
		want        string
		wantFailure bool
	}{
		{"split at spaces inverted empty", "regex:invert,literal ", splitSpaces, "", false},
		{"split at whitespace inverted empty", "regex:invert,literal ", strings.Fields, "", false},
		{"inverted match all", "regex:invert .*", strings.Fields, "", false},
		{"inverted dot keeps empty lines", "regex:invert .", strings.Fields, "\n" + sep, false},
		{"inverted literal", "regex:invert,literal alpha", strings.Fields, "\n" + sep + "beta\n" + sep, false},
		{"lone noop flags", "regex:noop ", strings.Fields, all, false},
		{"lone word which is no regex", ".", strings.Fields, all, false},
		{"lone word with regex prefix but no colon", "regexfoo", strings.Fields, all, false},
		{"lone unknown flag", "regex:bogus", strings.Fields, "", true},
		{"lone empty flag list", "regex:", strings.Fields, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeSharedReadFile(t, content)
			server := newSharedReadTestServer(t, nil)
			cmd := newReadCommandWithDependencies(server.readCommandDependencies(), omode.GrepClient, nil)
			args := tt.split("grep " + path + " " + tt.regex)

			cmd.Start(context.Background(), lcontext.LContext{}, len(args), args, 1)

			if got := drained(t, server); got != tt.want {
				t.Errorf("output = %q, want %q", got, tt.want)
			}
			checkReadFailures(t, server.failureMessages(), tt.wantFailure)
		})
	}
}

// checkReadFailures checks that the command reported exactly one failure to
// parse the command when wantFailure is set, and none otherwise.
func checkReadFailures(t *testing.T, failures []string, wantFailure bool) {
	t.Helper()
	if !wantFailure {
		if len(failures) != 0 {
			t.Errorf("failure messages = %q, want none", failures)
		}
		return
	}
	if len(failures) != 1 || !strings.HasSuffix(failures[0], protocol.CommandFailureReadCommand) {
		t.Errorf("failure messages = %q, want one ending in %q",
			failures, protocol.CommandFailureReadCommand)
	}
}

// splitSpaces splits a command at every space, keeping empty words.
func splitSpaces(command string) []string {
	return strings.Split(command, " ")
}
