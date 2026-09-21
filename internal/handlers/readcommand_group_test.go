package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/fs/readhub"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// infoRecorder records the hub's INFO lines; it is safe for concurrent use.
type infoRecorder struct {
	logging.NopLogger
	mu    sync.Mutex
	lines []string
}

func (l *infoRecorder) Info(args ...any) string {
	message := fmt.Sprint(args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, message)
	return message
}

func (l *infoRecorder) count(substrings ...string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, line := range l.lines {
		matches := true
		for _, s := range substrings {
			matches = matches && strings.Contains(line, s)
		}
		if matches {
			count++
		}
	}
	return count
}

func TestReadShareGroup(t *testing.T) {
	file := writeSharedReadFile(t, "x\n")
	fileTarget, err := fs.NewValidatedReadTarget(file)
	if err != nil {
		t.Fatal(err)
	}
	journalTarget, err := fs.NewValidatedJournalTarget("journal:dtail.service")
	if err != nil {
		t.Fatal(err)
	}
	readGroup := func(context.Context, omode.Mode, readhub.Session, readhub.Group) error { return nil }

	tests := []struct {
		name   string
		option string
		mutate func(*readCommand, **fs.ValidatedReadTarget)
		want   bool
	}{
		{"cat of a file with a hub", "g1:3", nil, true},
		{"grep", "g1:3", func(r *readCommand, _ **fs.ValidatedReadTarget) { r.mode = omode.GrepClient }, true},
		{"no option", "", nil, false},
		{"invalid option", "g1", nil, false},
		{"tail", "g1:3", func(r *readCommand, _ **fs.ValidatedReadTarget) { r.mode = omode.TailClient }, false},
		{"no hub", "g1:3", func(r *readCommand, _ **fs.ValidatedReadTarget) { r.readGroup = nil }, false},
		{"serverless", "g1:3", func(r *readCommand, _ **fs.ValidatedReadTarget) { r.serverless = true }, false},
		{"journal", "g1:3", func(_ *readCommand, target **fs.ValidatedReadTarget) { *target = &journalTarget }, false},
		{"no target", "g1:3", func(_ *readCommand, target **fs.ValidatedReadTarget) { *target = nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &readCommand{mode: omode.CatClient, readGroup: readGroup, logger: logging.NopLogger{}}
			target := &fileTarget
			if tt.mutate != nil {
				tt.mutate(r, &target)
			}
			group, ok := r.readShareGroup(withReadShareOption(context.Background(), tt.option), target)
			if ok != tt.want {
				t.Fatalf("readShareGroup() = %+v, %v, want %v", group, ok, tt.want)
			}
			if ok && (group.ID != "g1" || group.Members != 3) {
				t.Errorf("group = %+v, want g1 with 3 members", group)
			}
		})
	}
}

// groupReadCase is one session of a group, with its own filter.
type groupReadCase struct {
	name  string
	mode  omode.Mode
	ltx   lcontext.LContext
	regex string
}

// drained returns all output the server received once every line written so
// far went through its output collector.
func drained(t *testing.T, server *sharedReadTestServer) string {
	t.Helper()
	const marker = "\x00drained\x00"
	server.outputLines <- []byte(marker)
	waitForShared(t, "the output to drain", func() bool {
		return strings.HasSuffix(server.received(), marker)
	})
	return strings.TrimSuffix(server.received(), marker)
}

func runRead(t *testing.T, server *sharedReadTestServer, rc groupReadCase, path, share string) {
	t.Helper()
	cmd := newReadCommandWithDependencies(server.readCommandDependencies(), rc.mode, nil)
	target, ok := server.PrepareReadTarget(path)
	if !ok {
		t.Fatalf("test setup: no target for %s", path)
	}
	re, err := regex.New(rc.regex, regex.Default)
	if err != nil {
		t.Fatal(err)
	}
	cmd.read(withReadShareOption(context.Background(), share), rc.ltx, path, &target, "glob", re)
}

func TestGroupReadOutputEqualsPrivateReadOutput(t *testing.T) {
	var content strings.Builder
	for i := 1; i <= 2000; i++ {
		switch {
		case i%101 == 0:
			content.WriteString("\n")
		case i%9 == 0:
			fmt.Fprintf(&content, "ERROR line %d\n", i)
		default:
			fmt.Fprintf(&content, "INFO line %d\n", i)
		}
	}
	content.WriteString("ERROR unterminated last line")
	path := writeSharedReadFile(t, content.String())

	cases := []groupReadCase{
		{"cat", omode.CatClient, lcontext.LContext{}, ""},
		{"grep", omode.GrepClient, lcontext.LContext{}, "ERROR"},
		{"grep context", omode.GrepClient, lcontext.LContext{BeforeContext: 2, AfterContext: 1}, "ERROR"},
		{"grep max", omode.GrepClient, lcontext.LContext{MaxCount: 4}, "ERROR"},
		{"grep max context", omode.GrepClient, lcontext.LContext{MaxCount: 2, AfterContext: 3}, "ERROR"},
		{"cat max", omode.CatClient, lcontext.LContext{MaxCount: 7}, ""},
	}
	logger := &infoRecorder{}
	hub := newReadHub(&config.ServerConfig{}, logger)
	// Every mode is its own group read; each has as many members as cases.
	membersPerMode := map[omode.Mode]int{}
	for _, rc := range cases {
		membersPerMode[rc.mode]++
	}

	shared := make([]*sharedReadTestServer, len(cases))
	var wg sync.WaitGroup
	for i, rc := range cases {
		shared[i] = newSharedReadTestServer(t, hub)
		share := fmt.Sprintf("group-%s:%d", rc.mode, membersPerMode[rc.mode])
		wg.Add(1)
		go func() {
			defer wg.Done()
			runRead(t, shared[i], rc, path, share)
		}()
	}
	wg.Wait()

	for i, rc := range cases {
		private := newSharedReadTestServer(t, nil)
		runRead(t, private, rc, path, "")
		want := drained(t, private)
		if want == "" {
			t.Fatalf("%s: the private read produced no output", rc.name)
		}
		got := drained(t, shared[i])
		if got != want {
			t.Errorf("%s: group read output (%d bytes) differs from private read output (%d bytes)",
				rc.name, len(got), len(want))
		}
	}
	for mode, members := range membersPerMode {
		started := logger.count("Shared one-shot read started", "group=group-"+mode.String(),
			fmt.Sprintf("members=%d/%d", members, members))
		if started != 1 {
			t.Errorf("%s: %d group reads started with every member, want 1", mode, started)
		}
	}
}

func TestGroupReadEndings(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantPrivate bool
		wantPanic   bool
	}{
		{"late joiner reads privately", readhub.ErrGroupReadStarted, true, false},
		{"read ended", nil, false, false},
		{"reader error ends the read", errors.New("read failed"), false, false},
		{"reader panic", fmt.Errorf("%w: boom", fs.ErrReaderWorkerPanic), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeSharedReadFile(t, "existing\n")
			server := newSharedReadTestServer(t, nil)
			cmd := newReadCommandWithDependencies(server.readCommandDependencies(), omode.CatClient, nil)
			calls := 0
			cmd.readGroup = func(context.Context, omode.Mode, readhub.Session, readhub.Group) error {
				calls++
				return tt.err
			}
			target, _ := server.PrepareReadTarget(path)

			var recovered any
			func() {
				defer func() { recovered = recover() }()
				cmd.read(withReadShareOption(context.Background(), "g:2"), lcontext.LContext{}, path,
					&target, "glob", regex.NewNoop())
			}()

			if calls != 1 {
				t.Errorf("the group read was called %d times, want once", calls)
			}
			if (recovered != nil) != tt.wantPanic {
				t.Fatalf("recovered = %v, want panic %v", recovered, tt.wantPanic)
			}
			if tt.wantPrivate {
				waitForShared(t, "the private read", func() bool {
					return strings.Contains(server.received(), "existing")
				})
			} else if strings.Contains(drained(t, server), "existing") {
				t.Error("the file was read privately as well")
			}
		})
	}
}

func TestDispatchCommandKeepsTheReadShareOptionForTheCommand(t *testing.T) {
	share := config.ReadShare{Group: "abc", Members: 2}
	args := config.Args{ReadShare: share}
	var got string
	d := &commandDispatcher{
		handleCommandCb: func(ctx context.Context, _ lcontext.LContext, _ int, _ []string, _ string) {
			got, _ = ctx.Value(readShareKey).(string)
		},
	}
	command := "cat:" + args.SerializeOptions() + " /tmp/file.log noop"
	if err := d.handleRawCommand(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if got != share.String() {
		t.Errorf("read share option on the command context = %q, want %q", got, share.String())
	}

	got = "unset"
	if err := d.handleRawCommand(context.Background(), "cat:max=1 /tmp/file.log noop"); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("a command without the option got %q", got)
	}
}
