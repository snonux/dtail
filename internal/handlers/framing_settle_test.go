package handlers

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/session"
)

// settleTestClients are the sessions whose framing can still change when a
// command that can not change it arrives: an older client's first command,
// an escaped-only client's second one.
var settleTestClients = map[string][]string{
	"older client":        nil,
	"escaped-only client": {protocol.EscapedFramingCommand},
}

// TestServerHandlerSessionStartWithManyMissingFilesDoesNotDeadlock: a
// SESSION START whose reads queue more "Unable to read file(s)" messages
// than the server message channel holds acks its spec on the command input
// goroutine only once the channel has room again. The framing settles before
// that command is handled, so Read drains the channel meanwhile instead of
// holding every frame back until the command returned.
func TestServerHandlerSessionStartWithManyMissingFilesDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	files := make([]string, 60)
	for i := range files {
		files[i] = filepath.Join(dir, fmt.Sprintf("missing%d.log", i))
	}
	start, err := session.Spec{Mode: omode.CatClient, Files: files, Regex: "."}.StartCommand()
	if err != nil {
		t.Fatalf("build SESSION START: %v", err)
	}
	for name, first := range settleTestClients {
		t.Run(name, func(t *testing.T) {
			handler := newFramingTestServerHandler(t, ServerCapabilities("linux", false))
			output := &testOutput{}
			var writeMu sync.Mutex
			readerDone := startTestReader(handler, output, &writeMu)
			output.waitForContains(t, protocol.HiddenCapabilitiesPrefix, 5*time.Second)

			writeTestCommandsWithin(t, handler, &writeMu, 10*time.Second,
				append(append([]string(nil), first...), start)...)
			output.waitForContains(t, sessionAckStartOKPrefix, 5*time.Second)
			if !strings.Contains(output.String(), "Unable to read file(s)") {
				t.Fatalf("no missing file reported: %q", output.String())
			}
			handler.Shutdown()
			select {
			case <-readerDone:
			case <-time.After(5 * time.Second):
				t.Fatal("reader did not observe EOF after handler shutdown")
			}
		})
	}
}

// TestServerHandlerFailingFirstCommandsDoNotWaitForTheFlushTimeout: an
// unknown command or a failed map query that is a session's only command
// shuts the session down on the command input goroutine, flushing its
// output. The framing settles before that command is handled, so the flush
// does not wait for a Read held back until the command returned, which would
// take the full flush timeout and report a bogus flush error.
func TestServerHandlerFailingFirstCommandsDoNotWaitForTheFlushTimeout(t *testing.T) {
	commands := map[string]string{
		"unknown command":  "bogus command",
		"failed map query": "map: from STATS select count(a),count(a) group by $time",
	}
	for client, first := range settleTestClients {
		for name, command := range commands {
			t.Run(client+"/"+name, func(t *testing.T) {
				handler := newFramingTestServerHandler(t, ServerCapabilities("linux", false))
				stream := runFailingCommandSession(t, handler, first, command)
				if strings.Contains(stream, "Unable to flush session output") {
					t.Fatalf("shutdown flush waited for a held back Read: %q", stream)
				}
				if !strings.Contains(stream, ".syn close connection") {
					t.Fatalf("session ended without the close handshake: %q", stream)
				}
			})
		}
	}
}

// runFailingCommandSession sends the first command frames and then command,
// checks that the session ends well within the flush timeout, and returns
// all bytes sent to the client.
//
// The shutdown such a command starts runs on the command input goroutine,
// so a client's close acknowledgement, a later command frame, would only be
// handled once the acknowledgement timeout (5 s) passed, as before escaped
// framing existed. The test acknowledges the close directly instead, so the
// time it measures is the flush wait alone.
func runFailingCommandSession(t *testing.T, handler *ServerHandler, first []string,
	command string) string {
	t.Helper()
	output := &testOutput{}
	var writeMu sync.Mutex
	readerDone := startDirectAckTestReader(handler, output)
	output.waitForContains(t, protocol.HiddenCapabilitiesPrefix, 5*time.Second)

	started := time.Now()
	writeTestCommandsWithin(t, handler, &writeMu, 10*time.Second,
		append(append([]string(nil), first...), command)...)
	select {
	case <-readerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not observe EOF after handler shutdown")
	}
	if elapsed := time.Since(started); elapsed >= defaultOutputFlushTimeout/2 {
		t.Fatalf("session took %s to end, want no wait for the flush timeout", elapsed)
	}
	return output.String()
}

// startDirectAckTestReader reads handler's output into output like
// startTestReader, but acknowledges the close handshake directly rather
// than with a command frame.
func startDirectAckTestReader(handler *ServerHandler, output *testOutput) <-chan struct{} {
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		p := make([]byte, 4096)
		for {
			n, err := handler.Read(p)
			if n > 0 && strings.Contains(output.append(p[:n]), ".syn close connection") {
				handler.acknowledgeClose()
			}
			if errors.Is(err, io.EOF) {
				return
			}
		}
	}()
	return readerDone
}

// writeTestCommandsWithin writes commands as command frames in one write and
// fails the test unless the write returns within timeout, i.e. unless the
// command input goroutine handled them all.
func writeTestCommandsWithin(t *testing.T, handler *ServerHandler, writeMu *sync.Mutex,
	timeout time.Duration, commands ...string) {
	t.Helper()
	var frames strings.Builder
	for _, command := range commands {
		frames.WriteString(encodeTestCommand(command))
	}
	written := make(chan error, 1)
	go func() {
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err := handler.Write([]byte(frames.String()))
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("write commands: %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("command input goroutine blocked handling the commands")
	}
}
