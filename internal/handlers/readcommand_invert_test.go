package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

// TestReadCommandHonoursInvertedMatchAllPatterns runs a grep the way both
// transports hand it to readCommand.Start: a base64 command is split at every
// space, so an empty pattern stays an empty last word, while a SESSION command
// is split at runs of whitespace, which drops it. An inverted empty pattern
// must match no line either way rather than turn into a noop.
func TestReadCommandHonoursInvertedMatchAllPatterns(t *testing.T) {
	const content = "alpha\n\nbeta\n"
	sep := string([]byte{protocol.MessageDelimiter})
	all := "alpha\n" + sep + "\n" + sep + "beta\n" + sep

	tests := []struct {
		name  string
		regex string
		split func(string) []string
		want  string
	}{
		{"base64 inverted empty", "regex:invert,literal ", splitSpaces, ""},
		{"session inverted empty", "regex:invert,literal ", strings.Fields, ""},
		{"inverted match all", "regex:invert .*", strings.Fields, ""},
		{"inverted dot keeps empty lines", "regex:invert .", strings.Fields, "\n" + sep},
		{"inverted literal", "regex:invert,literal alpha", strings.Fields, "\n" + sep + "beta\n" + sep},
		{"session noop", "regex:noop ", strings.Fields, all},
		{"lone word which is no regex", ".", strings.Fields, all},
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
			if failures := server.failureMessages(); len(failures) != 0 {
				t.Errorf("failure messages = %q, want none", failures)
			}
		})
	}
}

// splitSpaces splits a command at every space, as a base64 command is.
func splitSpaces(command string) []string {
	return strings.Split(command, " ")
}
