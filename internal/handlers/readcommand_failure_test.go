package handlers

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

// failureTestServer is a globCapTestServer whose read targets are real,
// validated files, except for the denied paths.
type failureTestServer struct {
	*globCapTestServer
	denied map[string]bool
}

func (s *failureTestServer) PrepareReadTarget(path string) (fs.ValidatedReadTarget, bool) {
	if s.denied[path] {
		return fs.ValidatedReadTarget{}, false
	}
	target, err := fs.NewValidatedReadTarget(path)
	return target, err == nil
}

func (s *failureTestServer) readCommandDependencies() readCommandDependencies {
	dependencies := s.globCapTestServer.readCommandDependencies()
	dependencies.server = s
	return dependencies
}

// TestReadCommandReportsFailedReads runs one-shot read commands the way a
// scheduled mapreduce job's cat does and checks the hidden failed-command
// messages the client gets: exactly one, with the reason, for every way a
// read can fail, and none for a read that read all its files.
func TestReadCommandReportsFailedReads(t *testing.T) {
	tests := []struct {
		name string
		// setup creates the files in dir and returns the read's glob and the
		// paths PrepareReadTarget denies.
		setup      func(t *testing.T, dir string) (glob string, denied []string)
		args       func(glob string) []string
		maxTargets int
		want       string
	}{
		{
			name: "all files read",
			setup: func(t *testing.T, dir string) (string, []string) {
				writeFailureTestFile(t, filepath.Join(dir, "a.log"), 0o600)
				writeFailureTestFile(t, filepath.Join(dir, "b.log"), 0o600)
				return filepath.Join(dir, "*.log"), nil
			},
		},
		{
			name: "no file matches",
			setup: func(_ *testing.T, dir string) (string, []string) {
				return filepath.Join(dir, "missing-*.log"), nil
			},
			want: readFailureNoFile,
		},
		{
			name: "no permission to read a file",
			setup: func(t *testing.T, dir string) (string, []string) {
				writeFailureTestFile(t, filepath.Join(dir, "a.log"), 0o600)
				writeFailureTestFile(t, filepath.Join(dir, "b.log"), 0o600)
				return filepath.Join(dir, "*.log"), []string{filepath.Join(dir, "b.log")}
			},
			want: readFailurePermission,
		},
		{
			name: "no permission to read two files reports once",
			setup: func(t *testing.T, dir string) (string, []string) {
				writeFailureTestFile(t, filepath.Join(dir, "a.log"), 0o600)
				writeFailureTestFile(t, filepath.Join(dir, "b.log"), 0o600)
				return filepath.Join(dir, "*.log"), []string{filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")}
			},
			want: readFailurePermission,
		},
		{
			name: "file can not be opened",
			setup: func(t *testing.T, dir string) (string, []string) {
				if os.Geteuid() == 0 {
					t.Skip("root opens files without read permission")
				}
				writeFailureTestFile(t, filepath.Join(dir, "a.log"), 0o600)
				writeFailureTestFile(t, filepath.Join(dir, "b.log"), 0o000)
				return filepath.Join(dir, "*.log"), nil
			},
			want: readFailureReadingFile,
		},
		{
			name: "more files than the server reads",
			setup: func(t *testing.T, dir string) (string, []string) {
				for _, name := range []string{"a.log", "b.log", "c.log"} {
					writeFailureTestFile(t, filepath.Join(dir, name), 0o600)
				}
				return filepath.Join(dir, "*.log"), nil
			},
			maxTargets: 2,
			want:       readFailureGlobCapped,
		},
		{
			name: "unparsable command",
			setup: func(_ *testing.T, dir string) (string, []string) {
				return filepath.Join(dir, "*.log"), nil
			},
			args: func(glob string) []string { return []string{"cat", glob} },
			want: readFailureCommand,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			glob, denied := tt.setup(t, dir)
			maxTargets := tt.maxTargets
			if maxTargets == 0 {
				maxTargets = 100
			}
			srv := &failureTestServer{globCapTestServer: newGlobCapTestServer(maxTargets), denied: map[string]bool{}}
			for _, path := range denied {
				srv.denied[path] = true
			}
			args := []string{"cat", glob, "."}
			if tt.args != nil {
				args = tt.args(glob)
			}

			command := newReadCommand(srv, omode.CatClient)
			command.Start(context.Background(), lcontext.LContext{}, len(args), args, 1)

			var want []string
			if tt.want != "" {
				want = []string{protocol.HiddenCommandFailedPrefix + tt.want + "\n"}
			}
			if got := failedCommandMessages(srv.serverMessage); !slices.Equal(got, want) {
				t.Fatalf("failed command messages = %q, want %q", got, want)
			}
		})
	}
}

// TestReadCommandReportsNoFailureOnceCanceled checks that a read canceled
// with its session sends no failure: the session ends without its close
// handshake then, which the client already takes as incomplete.
func TestReadCommandReportsNoFailureOnceCanceled(t *testing.T) {
	srv := &failureTestServer{globCapTestServer: newGlobCapTestServer(100), denied: map[string]bool{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := newReadCommand(srv, omode.CatClient)
	command.reportFailure(ctx, readFailureReadingFile)
	if got := failedCommandMessages(srv.serverMessage); len(got) != 0 {
		t.Fatalf("failed command messages = %q, want none", got)
	}
}

// failedCommandMessages drains messages and returns the failed-command ones.
func failedCommandMessages(messages chan string) []string {
	var failed []string
	for {
		select {
		case message := <-messages:
			_, message = decodeGeneratedMessage(message)
			if strings.HasPrefix(message, protocol.HiddenCommandFailedPrefix) {
				failed = append(failed, message)
			}
		default:
			return failed
		}
	}
}

func writeFailureTestFile(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("line one\nline two\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}
