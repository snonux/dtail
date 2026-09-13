package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/clients"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/profiling"
	"github.com/mimecast/dtail/internal/source"
)

func TestBindCommonClientFlagsUsesProvidedFlagSet(t *testing.T) {
	fs := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var args config.Args
	runner := BindCommonClientFlags(fs, &args)

	err := fs.Parse([]string{
		"-agentKeyIndex", "3",
		"-auth-key-path", "/tmp/key",
		"-cfg", "none",
		"-cpuprofile",
		"-files", "/tmp/a.log",
		"-hostname-override", "test-host",
		"-known-hosts-path", "/tmp/known_hosts",
		"-logger", "stdout",
		"-port", "2022",
		"-profiledir", "/tmp/profiles",
	})
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if runner.fs != fs || runner.args != &args {
		t.Fatal("runner did not retain the provided flag set and args")
	}
	if got, want := args.SSHAgentKeyIndex, 3; got != want {
		t.Fatalf("SSHAgentKeyIndex = %d, want %d", got, want)
	}
	if got, want := args.SSHPrivateKeyFilePath, "/tmp/key"; got != want {
		t.Fatalf("SSHPrivateKeyFilePath = %q, want %q", got, want)
	}
	if got, want := args.HostnameOverride, "test-host"; got != want {
		t.Fatalf("HostnameOverride = %q, want %q", got, want)
	}
	if got, want := args.KnownHostsPath, "/tmp/known_hosts"; got != want {
		t.Fatalf("KnownHostsPath = %q, want %q", got, want)
	}
	if got, want := args.SSHPort, 2022; got != want {
		t.Fatalf("SSHPort = %d, want %d", got, want)
	}
	if !runner.profile.CPUProfile || runner.profile.ProfileDir != "/tmp/profiles" {
		t.Fatalf("profiling flags were not bound to the provided set: %+v", runner.profile)
	}
	if !FlagWasSet(fs, "auth-key-path") {
		t.Fatal("FlagWasSet did not inspect the provided flag set")
	}
	if got := fs.Lookup("agentKeyIndex"); got == nil || got.DefValue != "-1" {
		t.Fatalf("agentKeyIndex registration = %v, want default -1", got)
	}
}

func TestClientRunnerLifecycleAndArguments(t *testing.T) {
	fs := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var args config.Args
	runner := BindCommonClientFlags(fs, &args)
	events := make([]string, 0, 12)
	runner.BeforeSetup(func(args *config.Args) {
		events = append(events, "before-setup")
		args.RegexStr = "normalized"
	}).BeforeRuntime(func(*config.Args) (bool, int) {
		events = append(events, "before-runtime")
		return false, 0
	}).WithContext(func(config.Args) (context.Context, context.CancelFunc) {
		events = append(events, "context")
		return context.Background(), func() { events = append(events, "context-cancel") }
	}).AfterRuntime(func(*config.Args) (bool, int) {
		events = append(events, "after-runtime")
		return false, 0
	})

	runtime := &recordingClientRuntime{ctx: context.Background(), events: &events}
	stderr := &bytes.Buffer{}
	deps := clientDependenciesForTest(stderr)
	deps.argv = []string{"-cfg", "none", "first.log", "second.log"}
	deps.setup = func(gotSource source.Source, gotArgs *config.Args, additional []string) error {
		events = append(events, "setup")
		if gotSource != source.Client {
			t.Fatalf("setup source = %v, want %v", gotSource, source.Client)
		}
		if gotArgs.RegexStr != "normalized" {
			t.Fatalf("setup RegexStr = %q, want normalization before setup", gotArgs.RegexStr)
		}
		if want := []string{"first.log", "second.log"}; !reflect.DeepEqual(additional, want) {
			t.Fatalf("setup additional args = %q, want %q", additional, want)
		}
		return nil
	}
	deps.currentUserName = func() (string, error) {
		events = append(events, "user")
		return "alice", nil
	}
	deps.newRuntime = func(context.Context, profiling.Flags, string) (clientRuntime, error) {
		events = append(events, "new-runtime")
		return runtime, nil
	}
	deps.interrupt = func(context.Context, context.CancelFunc) <-chan string {
		events = append(events, "interrupt")
		return make(chan string)
	}
	deps.loggers = func() clients.LoggerDependencies {
		events = append(events, "loggers")
		return clients.LoggerDependencies{}
	}

	status := runner.runClient("test-client", func(got config.Args, _ clients.LoggerDependencies) (clients.Client, error) {
		events = append(events, "build")
		if got.UserName != "alice" {
			t.Fatalf("build UserName = %q, want alice", got.UserName)
		}
		return clientFunc(func(context.Context, <-chan string) int {
			events = append(events, "start")
			return 7
		}), nil
	}, deps)

	if status != 7 {
		t.Fatalf("runClient status = %d, want 7", status)
	}
	wantEvents := []string{
		"before-setup", "setup", "user", "before-runtime", "context",
		"new-runtime", "after-runtime", "pprof", "startup", "loggers", "build",
		"interrupt", "start", "shutdown", "stop", "context-cancel",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("lifecycle events = %q, want %q", events, wantEvents)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestClientRunnerSetupErrorStopsBeforeRuntime(t *testing.T) {
	runner, stderr := newTestClientRunner(t)
	wantErr := errors.New("bad config")
	deps := clientDependenciesForTest(stderr)
	deps.setup = func(source.Source, *config.Args, []string) error { return wantErr }
	newRuntimeCalled := false
	deps.newRuntime = func(context.Context, profiling.Flags, string) (clientRuntime, error) {
		newRuntimeCalled = true
		return nil, nil
	}

	status := runner.runClient("dcat", func(config.Args, clients.LoggerDependencies) (clients.Client, error) {
		t.Fatal("build called after setup failure")
		return nil, nil
	}, deps)

	if status != 1 {
		t.Fatalf("runClient status = %d, want 1", status)
	}
	if newRuntimeCalled {
		t.Fatal("runtime started after setup failure")
	}
	if got := stderr.String(); !strings.Contains(got, "unable to configure dcat: bad config") {
		t.Fatalf("stderr = %q, want contextual setup error", got)
	}
}

func TestClientRunnerBuildErrorCleansUpRuntimeAndContext(t *testing.T) {
	runner, stderr := newTestClientRunner(t)
	events := []string{}
	runner.WithContext(func(config.Args) (context.Context, context.CancelFunc) {
		return context.Background(), func() { events = append(events, "context-cancel") }
	})
	runtime := &recordingClientRuntime{ctx: context.Background(), events: &events}
	deps := clientDependenciesForTest(stderr)
	deps.newRuntime = func(context.Context, profiling.Flags, string) (clientRuntime, error) {
		return runtime, nil
	}
	deps.logBuildError = func(name string, err error) {
		events = append(events, "log-error")
	}
	wantErr := errors.New("constructor failed")

	status := runner.runClient("dmap", func(config.Args, clients.LoggerDependencies) (clients.Client, error) {
		return nil, wantErr
	}, deps)

	if status != 1 {
		t.Fatalf("runClient status = %d, want 1", status)
	}
	wantEvents := []string{"pprof", "startup", "log-error", "stop", "context-cancel"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("cleanup events = %q, want %q", events, wantEvents)
	}
	if got := stderr.String(); !strings.Contains(got, "unable to create dmap client: constructor failed") {
		t.Fatalf("stderr = %q, want contextual constructor error", got)
	}
}

func TestClientRunnerAfterRuntimeCanHandleCommand(t *testing.T) {
	runner, stderr := newTestClientRunner(t)
	events := []string{}
	runner.WithContext(func(config.Args) (context.Context, context.CancelFunc) {
		return context.Background(), func() { events = append(events, "context-cancel") }
	}).AfterRuntime(func(*config.Args) (bool, int) {
		events = append(events, "handled")
		return true, 9
	})
	runtime := &recordingClientRuntime{ctx: context.Background(), events: &events}
	deps := clientDependenciesForTest(stderr)
	deps.newRuntime = func(context.Context, profiling.Flags, string) (clientRuntime, error) {
		return runtime, nil
	}

	status := runner.runClient("dtail", func(config.Args, clients.LoggerDependencies) (clients.Client, error) {
		t.Fatal("build called after command was handled")
		return nil, nil
	}, deps)

	if status != 9 {
		t.Fatalf("runClient status = %d, want 9", status)
	}
	wantEvents := []string{"handled", "stop", "context-cancel"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("handled events = %q, want %q", events, wantEvents)
	}
}

type clientFunc func(context.Context, <-chan string) int

func (f clientFunc) Start(ctx context.Context, stats <-chan string) int {
	return f(ctx, stats)
}

type recordingClientRuntime struct {
	ctx    context.Context
	events *[]string
}

func (r *recordingClientRuntime) Context() context.Context { return r.ctx }
func (r *recordingClientRuntime) Cancel()                  {}
func (r *recordingClientRuntime) StartPProf(string) {
	*r.events = append(*r.events, "pprof")
}
func (r *recordingClientRuntime) LogStartupMetrics() {
	*r.events = append(*r.events, "startup")
}
func (r *recordingClientRuntime) LogShutdownMetrics() {
	*r.events = append(*r.events, "shutdown")
}
func (r *recordingClientRuntime) Stop() {
	*r.events = append(*r.events, "stop")
}

func newTestClientRunner(t *testing.T) (*ClientRunner, *bytes.Buffer) {
	t.Helper()
	fs := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	stderr := &bytes.Buffer{}
	fs.SetOutput(stderr)
	var args config.Args
	return BindCommonClientFlags(fs, &args), stderr
}

func clientDependenciesForTest(stderr io.Writer) clientRunDependencies {
	return clientRunDependencies{
		stderr: stderr,
		setup: func(source.Source, *config.Args, []string) error {
			return nil
		},
		currentUserName: func() (string, error) { return "test-user", nil },
		colorsEnabled:   func() bool { return false },
		printVersion:    func(bool) {},
		newRuntime: func(context.Context, profiling.Flags, string) (clientRuntime, error) {
			return &recordingClientRuntime{ctx: context.Background(), events: &[]string{}}, nil
		},
		interrupt:     func(context.Context, context.CancelFunc) <-chan string { return make(chan string) },
		logBuildError: func(string, error) {},
	}
}
