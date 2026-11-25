package clients

import (
	"bufio"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestControlInputFIFOSurvivesItsWriters checks that a FIFO given as the
// control input does not end when a program that wrote one request closes
// it (an agent's `echo '{...}' > fifo` per request): it is opened for
// reading and writing, so a second writer's request is read too. The output
// file outlives the input's close.
func TestControlInputFIFOSurvivesItsWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	in, out, closeIn, err := openControlChannel(path, filepath.Join(t.TempDir(), "out.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeIn()
	if file, ok := out.(*os.File); ok {
		defer func() { _ = file.Close() }()
	}
	if out == os.Stderr {
		t.Fatal("the control output path was not opened")
	}
	reader := bufio.NewReader(in)
	for _, request := range []string{"{\"cmd\":\"show\"}\n", "{\"cmd\":\"quit\"}\n"} {
		if err := os.WriteFile(path, []byte(request), 0o600); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadString('\n')
		if err != nil || line != request {
			t.Fatalf("read %q, %v; want %q", line, err, request)
		}
	}
	// Closing the input leaves the output open: the last result of a query
	// is written after the control loop ended.
	closeIn()
	if _, err := out.Write([]byte("{}\n")); err != nil {
		t.Fatalf("write to the control output after the input was closed: %v", err)
	}
	if _, _, _, err := openControlChannel(filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Fatal("a missing control input was opened")
	}
}
