package client

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/logging"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	// trustAllMaxWait bounds how long a trust-all handshake may wait for its
	// unknown host to be trusted. It is far above trustAllHostsBatchDelay, to
	// stay robust on a loaded machine, and far below unknownHostsPromptDelay,
	// so a regression to the interactive debounce fails the test.
	trustAllMaxWait = time.Second
	// interactiveMaxWait bounds how long an interactive handshake may wait
	// for the prompt to be answered: the debounce plus generous slack for a
	// loaded machine.
	interactiveMaxWait = unknownHostsPromptDelay + 10*time.Second
	// parkWait is how long a test waits for its handshake goroutines to block
	// on sending their unknown host before it starts PromptAddHosts. It only
	// needs to cover a parse of a tiny known_hosts file per goroutine.
	parkWait = 200 * time.Millisecond
)

func TestBatchTimerDelay(t *testing.T) {
	tests := []struct {
		name      string
		trustAll  bool
		pending   int
		wantDelay time.Duration
		wantArm   bool
	}{
		{"interactive first host", false, 1, unknownHostsPromptDelay, true},
		{"interactive later host restarts", false, 2, unknownHostsPromptDelay, true},
		{"trust-all first host", true, 1, trustAllHostsBatchDelay, true},
		{"trust-all later host keeps window", true, 2, trustAllHostsBatchDelay, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			callback := newBatchTestCallback(t, test.trustAll)
			delay, arm := callback.batchTimerDelay(test.pending)
			if delay != test.wantDelay || arm != test.wantArm {
				t.Fatalf("batchTimerDelay(%d) = (%v, %v), want (%v, %v)",
					test.pending, delay, arm, test.wantDelay, test.wantArm)
			}
		})
	}
}

// TestPromptAddHostsTrustAllAnswersQuickly verifies that in trust-all mode
// unknown hosts are trusted without waiting for the 2 s interactive prompt
// debounce, all of them within one overall deadline, and that hosts arriving
// together are written to known_hosts in a single update. The handshake
// goroutines get parkWait to block on sending their host before
// PromptAddHosts starts, so it receives all of them back to back, well
// within the 20 ms window, without depending on how promptly a loaded
// machine schedules the goroutines afterwards.
func TestPromptAddHostsTrustAllAnswersQuickly(t *testing.T) {
	for _, hosts := range []int{1, 3} {
		t.Run(fmt.Sprintf("%d hosts", hosts), func(t *testing.T) {
			logger := &trustBatchCountingLogger{}
			callback := newBatchTestCallbackWithLogger(t, true, logger)
			// The Wrap callbacks start before PromptAddHosts, so they get a
			// ctx of their own; like the PromptAddHosts ctx, the cleanup
			// cancels it.
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			errCh := make(chan error, hosts)
			for i := range hosts {
				go func() { errCh <- callUnknownHost(ctx, callback, i) }()
			}
			time.Sleep(parkWait)
			start := time.Now()
			_, _ = startPromptAddHosts(t, callback)

			waitForHosts(t, errCh, hosts, time.After(trustAllMaxWait), "trust-all")
			t.Logf("%d unknown hosts trusted after %v", hosts, time.Since(start))
			assertKnownHostsLines(t, callback.knownHostsPath, batchHostLines(hosts))
			if batches := logger.batches.Load(); batches != 1 {
				t.Fatalf("%d hosts were written in %d known_hosts updates, want 1", hosts, batches)
			}
		})
	}
}

// TestPromptAddHostsTrustAllWindowIsNotRestarted streams trust-all hosts
// every few milliseconds for longer than trustAllStreamDuration. Since later
// hosts do not restart the coalescing window, the first handshake is trusted
// long before the stream ends; a window that every host restarts would hold
// all of them until the stream stopped.
func TestPromptAddHostsTrustAllWindowIsNotRestarted(t *testing.T) {
	const (
		interval = 8 * time.Millisecond
		// trustAllStreamDuration is how long hosts keep arriving.
		trustAllStreamDuration = 300 * time.Millisecond
		// firstMaxWait is far below trustAllStreamDuration and far above
		// trustAllHostsBatchDelay, to stay robust on a loaded machine.
		firstMaxWait = 200 * time.Millisecond
		// maxHosts stays below unknownHostsMaxBatch, so the batch cap can
		// never flush a batch and hide a restarted window.
		maxHosts = unknownHostsMaxBatch - 5
	)
	callback := newBatchTestCallback(t, true)
	ctx, _ := startPromptAddHosts(t, callback)

	type result struct {
		err error
		at  time.Time
	}
	results := make(chan result, maxHosts)
	start := time.Now()
	hosts := 0
	for ; hosts < maxHosts && time.Since(start) < trustAllStreamDuration; hosts++ {
		go func(i int) {
			err := callUnknownHost(ctx, callback, i)
			results <- result{err: err, at: time.Now()}
		}(hosts)
		time.Sleep(interval)
	}
	streamEnd := time.Since(start)

	var first time.Duration
	deadline := time.After(trustAllMaxWait)
	for i := range hosts {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("trust-all host key callback failed: %v", r.err)
			}
			if at := r.at.Sub(start); i == 0 || at < first {
				first = at
			}
		case <-deadline:
			t.Fatalf("trust-all host key callbacks did not return within %v after the stream", trustAllMaxWait)
		}
	}
	t.Logf("%d hosts streamed over %v, first trusted after %v", hosts, streamEnd, first)
	if first >= firstMaxWait {
		t.Fatalf("first trust-all host returned after %v, want < %v (stream lasted %v)",
			first, firstMaxWait, streamEnd)
	}
	assertKnownHostsLines(t, callback.knownHostsPath, batchHostLines(hosts))
}

// TestPromptAddHostsBatchCap verifies that the interactive prompt does not
// wait for its debounce once unknownHostsMaxBatch hosts are pending: all of
// them are answered before the debounce could have fired even once, which is
// unknownHostsPromptDelay after the last of them arrived and so after start.
// One more host then starts a new batch and stays pending.
func TestPromptAddHostsBatchCap(t *testing.T) {
	callback := newBatchTestCallback(t, false)
	withPromptInput(t, "yes\n", func() {
		ctx, stop := startPromptAddHosts(t, callback)
		// Stop before withPromptInput restores os.Stdin, which the prompt reads.
		defer stop()

		start := time.Now()
		errCh := make(chan error, unknownHostsMaxBatch)
		for i := range unknownHostsMaxBatch {
			go func() { errCh <- callUnknownHost(ctx, callback, i) }()
		}
		deadline := time.After(unknownHostsPromptDelay)
		waitForHosts(t, errCh, unknownHostsMaxBatch, deadline, "batch cap")
		t.Logf("%d unknown hosts trusted after %v", unknownHostsMaxBatch, time.Since(start))
		assertKnownHostsLines(t, callback.knownHostsPath, batchHostLines(unknownHostsMaxBatch))

		assertNextHostStaysPending(t, callback, stop)
	})
}

// assertNextHostStaysPending hands PromptAddHosts one more host directly. The
// unbuffered unknownCh send returns once PromptAddHosts holds the host, which
// now waits for the 2 s debounce; it must stay unanswered until PromptAddHosts
// is stopped.
func assertNextHostStaysPending(t *testing.T, callback *KnownHostsCallback, stop func()) {
	t.Helper()
	next := testUnknownHost("batch-next.example:2222", "batch-next")
	select {
	case callback.unknownCh <- next:
	case <-time.After(time.Second):
		t.Fatal("PromptAddHosts did not take the next unknown host")
	}
	stop()
	select {
	case resp := <-next.responseCh:
		t.Fatalf("host after a full batch was answered with %v, want it pending", resp)
	default:
	}
}

// TestPromptAddHostsAllAnswerSwitchesToTrustAll answers "all" for the first
// unknown host, after the interactive debounce, and checks that a following
// host is then trusted within the short trust-all window.
func TestPromptAddHostsAllAnswerSwitchesToTrustAll(t *testing.T) {
	callback := newBatchTestCallback(t, false)
	withPromptInput(t, "all\n", func() {
		ctx, stop := startPromptAddHosts(t, callback)
		// Stop before withPromptInput restores os.Stdin, which the prompt reads.
		defer stop()

		errCh := make(chan error, 2)
		go func() { errCh <- callUnknownHost(ctx, callback, 0) }()
		waitForHosts(t, errCh, 1, time.After(interactiveMaxWait), "interactive")
		// Deterministic: the "all" answer enables trust-all mode before it
		// lets the handshake return (see allAnswer).
		if !callback.trustAll() {
			t.Fatal("trust-all mode is off after an \"all\" answer")
		}

		start := time.Now()
		go func() { errCh <- callUnknownHost(ctx, callback, 1) }()
		waitForHosts(t, errCh, 1, time.After(trustAllMaxWait), "trust-all after \"all\"")
		t.Logf("host after \"all\" trusted after %v", time.Since(start))
		assertKnownHostsLines(t, callback.knownHostsPath, batchHostLines(2))
	})
}

// TestPromptAddHostsInteractiveAsksAfterDebounce is the positive interactive
// case: the unknown host is asked about once the debounce expired (never
// before), and a "yes" trusts it.
func TestPromptAddHostsInteractiveAsksAfterDebounce(t *testing.T) {
	callback := newBatchTestCallback(t, false)
	withPromptInput(t, "yes\n", func() {
		ctx, stop := startPromptAddHosts(t, callback)
		// Stop before withPromptInput restores os.Stdin, which the prompt reads.
		defer stop()

		start := time.Now()
		errCh := make(chan error, 1)
		go func() { errCh <- callUnknownHost(ctx, callback, 0) }()
		waitForHosts(t, errCh, 1, time.After(interactiveMaxWait), "interactive")
		if waited := time.Since(start); waited < unknownHostsPromptDelay {
			t.Fatalf("interactive host was trusted after %v, before the %v debounce",
				waited, unknownHostsPromptDelay)
		}
		assertKnownHostsLines(t, callback.knownHostsPath, batchHostLines(1))
	})
}

// TestPromptAddHostsInteractiveKeepsDebounce is the negative counterpart:
// without trust-all mode the unknown host still waits for the interactive
// debounce, and cancelling ctx during that wait aborts the handshake callback
// and stops PromptAddHosts. Nothing should be asked, but stdin is replaced by
// an empty input anyway: should the test be delayed past the debounce, the
// prompt reads EOF (answering "no") instead of the real stdin.
func TestPromptAddHostsInteractiveKeepsDebounce(t *testing.T) {
	callback := newBatchTestCallback(t, false)
	withPromptInput(t, "", func() {
		ctx, stop := startPromptAddHosts(t, callback)
		// Stop before withPromptInput restores os.Stdin, which the prompt reads.
		defer stop()

		errCh := make(chan error, 1)
		go func() { errCh <- callUnknownHost(ctx, callback, 0) }()

		select {
		case err := <-errCh:
			t.Fatalf("interactive host key callback returned before the prompt debounce: %v", err)
		case <-time.After(unknownHostsPromptDelay / 4):
		}
		stop()

		select {
		case err := <-errCh:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled after cancel, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("interactive host key callback did not return after ctx cancel")
		}
		assertKnownHostsEmpty(t, callback.knownHostsPath)
	})
}

// trustBatchCountingLogger counts the trust-all batches promptAddHosts
// writes to known_hosts, one "Trusting host keys of servers" entry each.
type trustBatchCountingLogger struct {
	logging.NopLogger
	batches atomic.Int32
}

func (l *trustBatchCountingLogger) Debug(args ...any) string {
	if len(args) > 0 && args[0] == "Trusting host keys of servers" {
		l.batches.Add(1)
	}
	return ""
}

func newBatchTestCallback(t *testing.T, trustAll bool) *KnownHostsCallback {
	t.Helper()
	return newBatchTestCallbackWithLogger(t, trustAll, sshClientTestLogger)
}

func newBatchTestCallbackWithLogger(t *testing.T, trustAll bool,
	logger logging.Logger) *KnownHostsCallback {

	t.Helper()
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHostsPath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	callback, err := NewKnownHostsCallback(knownHostsPath, trustAll, logger, sshClientTestLogger)
	if err != nil {
		t.Fatalf("NewKnownHostsCallback failed: %v", err)
	}
	return callback.(*KnownHostsCallback)
}

// startPromptAddHosts runs PromptAddHosts until the returned stop function
// is called; stop cancels the returned ctx and waits for PromptAddHosts to
// return. stop is idempotent and also registered as a cleanup of t, so a
// failing test never leaks the goroutine into later tests (which may swap
// os.Stdin). Tests pass ctx to their Wrap callbacks as well: production
// cancels the Wrap ctx together with the PromptAddHosts ctx, so the callbacks
// still pending at a stop return ctx.Err().
func startPromptAddHosts(t *testing.T, callback *KnownHostsCallback) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		callback.PromptAddHosts(ctx)
	}()
	stop := sync.OnceFunc(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("PromptAddHosts did not return after ctx cancel")
		}
	})
	t.Cleanup(stop)
	return ctx, stop
}

// waitForHosts waits for hosts results of handshake callbacks on errCh, each
// of which must be nil, until deadline fires once for all of them.
func waitForHosts(t *testing.T, errCh <-chan error, hosts int,
	deadline <-chan time.Time, mode string) {

	t.Helper()
	for i := range hosts {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("%s host key callback failed: %v", mode, err)
			}
		case <-deadline:
			t.Fatalf("only %d of %d %s host key callbacks returned before the deadline",
				i, hosts, mode)
		}
	}
}

// callUnknownHost runs the wrapped host key callback for host number i, with
// a server name, address and key that no other host number uses.
func callUnknownHost(ctx context.Context, callback *KnownHostsCallback, i int) error {
	server, remote, key := batchHost(i)
	return callback.Wrap(ctx)(server, remote, key)
}

// batchHost returns the server name, address and host key of host number i.
// The key is a real ed25519 key derived from i, not a mockPublicKey: hosts
// that arrive after a known_hosts update make Wrap parse the lines written
// for earlier hosts, which only works for keys that unmarshal again.
func batchHost(i int) (string, *net.TCPAddr, ssh.PublicKey) {
	seed := make([]byte, ed25519.SeedSize)
	binary.BigEndian.PutUint32(seed, uint32(i))
	key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(seed).Public())
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("batch%d.example:2222", i),
		testTCPAddr(fmt.Sprintf("127.0.0.1:%d", 2300+i)), key
}

// batchHostLines returns the known_hosts host and IP lines of the first
// hosts host numbers of callUnknownHost.
func batchHostLines(hosts int) []string {
	var lines []string
	for i := range hosts {
		server, remote, key := batchHost(i)
		lines = append(lines, knownhosts.Line([]string{server}, key),
			knownhosts.Line([]string{remote.String()}, key))
	}
	return lines
}

// assertKnownHostsEmpty checks that no host was added to known_hosts.
func assertKnownHostsEmpty(t *testing.T, knownHostsPath string) {
	t.Helper()
	data, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("known_hosts is not empty:\n%s", data)
	}
}
