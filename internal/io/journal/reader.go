//go:build linux

// Package journal provides a journalctl-backed file reader.
package journal

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

const (
	defaultSourceID       = "journal"
	journalctlCommand     = "journalctl"
	maxScannerTokenSize   = 1024 * 1024
	processTerminateGrace = 200 * time.Millisecond
)

var errStopReading = errors.New("stop journal reading")

// ErrJournalctlNotFound reports that journalctl could not be found on PATH.
var ErrJournalctlNotFound = errors.New("journalctl not found")

// Reader reads journal entries by executing journalctl.
type Reader struct {
	journalctlPath string
	args           []string
	sourceID       string
	serverMessages chan<- string
	follow         bool
}

var _ fs.FileReader = (*Reader)(nil)

// NewReader returns a journalctl-backed file reader.
func NewReader(args []string, sourceID string, follow bool, serverMessages chan<- string) (*Reader, error) {
	journalctlPath, err := exec.LookPath(journalctlCommand)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrJournalctlNotFound, err)
	}
	if sourceID == "" {
		sourceID = defaultSourceID
	}

	copiedArgs := append([]string(nil), args...)
	return &Reader{
		journalctlPath: journalctlPath,
		args:           copiedArgs,
		sourceID:       sourceID,
		serverMessages: serverMessages,
		follow:         follow,
	}, nil
}

// StartWithProcessor reads journalctl stdout and sends matching lines to processor.
func (r *Reader) StartWithProcessor(ctx context.Context, ltx lcontext.LContext,
	processor line.Processor, re regex.Regex) error {

	return r.runWithProcessor(ctx, ltx, processor, re)
}

// StartWithProcessorOptimized reads journalctl stdout and sends matching lines to processor.
func (r *Reader) StartWithProcessorOptimized(ctx context.Context, ltx lcontext.LContext,
	processor line.Processor, re regex.Regex) error {

	return r.runWithProcessor(ctx, ltx, processor, re)
}

// FilePath returns a stable journalctl command description.
func (r *Reader) FilePath() string {
	if len(r.args) == 0 {
		return journalctlCommand
	}
	return journalctlCommand + " " + strings.Join(r.args, " ")
}

// Retry reports whether journalctl should be restarted after it exits.
func (r *Reader) Retry() bool {
	return r.follow
}

func (r *Reader) runWithProcessor(ctx context.Context, ltx lcontext.LContext,
	processor line.Processor, re regex.Regex) error {

	sink := processorSink{processor: processor}

	// In follow mode r.run blocks until journalctl is stopped, so a batching
	// processor (the NetworkWriter, which buffers up to 64KB before
	// sending) would hold live lines in its buffer and the client would never
	// see interactive output. Flush after every scanned line while following so
	// journal follow output reaches the client promptly — the same latency
	// guarantee the file follow path gets from its per-read-chunk flush. A
	// non-follow snapshot read keeps the batching benefit and flushes once at
	// the end below.
	var flushLine func() error
	if r.follow {
		flushLine = processor.Flush
	}

	err := r.run(ctx, ltx, sink, re, flushLine)
	if flushErr := processor.Flush(); flushErr != nil && err == nil {
		err = flushErr
	}
	return err
}

func (r *Reader) run(ctx context.Context, ltx lcontext.LContext, sink journalSink,
	re regex.Regex, flushLine func() error) error {

	cmd := r.command(ctx)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open journalctl stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open journalctl stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start journalctl: %w", err)
	}

	stderrDone := make(chan error, 1)
	go func() {
		stderrDone <- r.forwardStderr(ctx, stderr)
	}()

	filter := newJournalFilter(ltx, sink, re, r.sourceID)
	scanErr := r.scanStdout(ctx, stdout, filter, flushLine)
	waitErr := waitForJournalctl(cmd, scanErr != nil)
	stderrErr := <-stderrDone
	filter.Close()

	if ctx.Err() != nil || errors.Is(scanErr, errStopReading) {
		return nil
	}
	if scanErr != nil {
		return scanErr
	}
	if stderrErr != nil {
		return stderrErr
	}
	if waitErr != nil {
		return fmt.Errorf("journalctl failed: %w", waitErr)
	}
	return nil
}

func (r *Reader) command(ctx context.Context) *exec.Cmd {
	args := r.commandArgs()
	cmd := exec.CommandContext(ctx, r.journalctlPath, args...)
	cmd.Cancel = func() error {
		terminateProcess(cmd.Process)
		return nil
	}
	cmd.WaitDelay = processTerminateGrace
	return cmd
}

func (r *Reader) commandArgs() []string {
	args := append([]string(nil), r.args...)
	if r.follow {
		args = append(args, "-f", "-n", "0")
	}
	return args
}

func terminateProcess(process *os.Process) {
	if process == nil {
		return
	}
	_ = process.Signal(syscall.SIGTERM)
}

func killProcess(process *os.Process) {
	if process == nil {
		return
	}
	_ = process.Kill()
}

func waitForJournalctl(cmd *exec.Cmd, earlyStop bool) error {
	if !earlyStop {
		return cmd.Wait()
	}

	terminateProcess(cmd.Process)

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	timer := time.NewTimer(processTerminateGrace)
	defer timer.Stop()

	select {
	case err := <-waitDone:
		return err
	case <-timer.C:
		killProcess(cmd.Process)
		return <-waitDone
	}
}

func (r *Reader) scanStdout(ctx context.Context, stdout io.Reader, filter *journalFilter,
	flushLine func() error) error {

	scanner := bufio.NewScanner(stdout)
	bufPtr := pool.GetScannerBuffer()
	defer pool.PutScannerBuffer(bufPtr)

	scanner.Buffer(*bufPtr, maxScannerTokenSize)
	scanner.Split(scanLinesPreserveEndings)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		lineBuf := pool.BytesBuffer.Get().(*bytes.Buffer)
		lineBuf.Write(scanner.Bytes())
		if err := filter.Process(ctx, lineBuf); err != nil {
			return err
		}

		// In follow mode, push any buffered line straight to the client so
		// batching never delays live output. flushLine is nil for non-follow
		// reads (they batch and flush once at the end) and for the immediate
		// channel sink.
		if flushLine != nil {
			if err := flushLine(); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan journalctl stdout: %w", err)
	}
	return nil
}

func (r *Reader) forwardStderr(ctx context.Context, stderr io.Reader) error {
	scanner := bufio.NewScanner(stderr)
	bufPtr := pool.GetScannerBuffer()
	defer pool.PutScannerBuffer(bufPtr)

	scanner.Buffer(*bufPtr, maxScannerTokenSize)
	for scanner.Scan() {
		if !r.sendServerMessage(ctx, fmt.Sprintf("journalctl stderr: %s\n", scanner.Text())) {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, os.ErrClosed) {
			return nil
		}
		return fmt.Errorf("scan journalctl stderr: %w", err)
	}
	return nil
}

func (r *Reader) sendServerMessage(ctx context.Context, message string) bool {
	if r.serverMessages == nil {
		return true
	}
	select {
	case r.serverMessages <- message:
		return true
	case <-ctx.Done():
		return false
	}
}

func scanLinesPreserveEndings(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF {
		return len(data), nil, nil
	}
	return 0, nil, nil
}
