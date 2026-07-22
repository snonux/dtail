package handlers

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/ctxutil"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/journal"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

type readCommand struct {
	server              readCommandServer
	mode                omode.Mode
	generation          uint64
	shutdownCoordinator *shutdownCoordinator
}

type readStrategy func(context.Context, lcontext.LContext, fs.FileReader, regex.Regex) error

type readProcessor interface {
	ProcessLine(*bytes.Buffer, uint64, string) error
	Flush() error
	Close() error
}

func newReadCommand(server readCommandServer, mode omode.Mode) *readCommand {
	// cat/grep reads are one-shot: their input is exhausted once every file
	// has been read to EOF. tail follows its files indefinitely, so its input
	// never exhausts and must not finish a output aggregate.
	oneShotInput := mode == omode.CatClient || mode == omode.GrepClient
	return &readCommand{
		server:              server,
		mode:                mode,
		shutdownCoordinator: newShutdownCoordinator(server, oneShotInput),
	}
}

func (r *readCommand) Start(ctx context.Context, ltx lcontext.LContext,
	argc int, args []string, retries int) {
	r.generation = sessionGenerationFromContext(ctx)

	re := regex.NewNoop()
	if argc >= 4 {
		deserializedRegex, err := regex.Deserialize(strings.Join(args[2:], " "))
		if err != nil {
			r.sendServerMessage(ctx, dlog.Server.Error(r.server.LogContext(),
				"Unable to parse command", err))
			return
		}
		re = deserializedRegex
	}
	if argc < 3 {
		r.sendServerMessage(ctx, dlog.Server.Warn(r.server.LogContext(),
			"Unable to parse command", args, argc))
		return
	}

	// In serverless mode, can also read data from pipe
	// e.g.: grep foo bar.log | dmap 'from STATS select ...'
	// Only read from pipe if no file argument is provided
	isPipe := r.isInputFromPipe() && (argc < 2 || args[1] == "" || args[1] == "-")

	if isPipe {
		dlog.Server.Debug("Reading data from stdin pipe")
		r.readPipe(ctx, ltx, re)
		return
	}

	if fs.IsJournalSpec(args[1]) {
		dlog.Server.Debug("Reading data from journal")
		r.readJournal(ctx, ltx, args[1], re, retries)
		return
	}

	dlog.Server.Debug("Reading data from file(s)")
	r.readGlob(ctx, ltx, args[1], re, retries)
}

// readPipe reads the single stdin-pipe input in serverless mode (e.g.
// `grep foo bar.log | dmap 'from STATS select ...'`). Unlike file/glob/journal
// reads it does NOT go through readFiles/readFileIfPermissions, so it must
// reproduce their finalization here: the pipe is accounted as one pending input
// and onFileProcessed is invoked once it drains. That is what drives the
// shutdown coordinator's FinishInput signal to a output aggregate — without it a
// serverless dmap reading from a pipe would block in Aggregate.Start on its
// inputFinished signal forever (the client keeps re-gathering interim results
// and never terminates). Previously the pipe fed a regular Aggregate that
// finalized when its input line channel was closed; the output aggregate has no
// such channel-close, so the pending-file/onFileProcessed path is the
// equivalent "input exhausted" signal. The pending accounting also keeps the
// counter balanced: onFileProcessed unconditionally decrements pendingFiles, so
// it must be paired with an AddPendingFiles here.
func (r *readCommand) readPipe(ctx context.Context, ltx lcontext.LContext, re regex.Regex) {
	r.server.AddPendingFiles(1)
	defer r.shutdownCoordinator.onFileProcessed("-")
	// Empty file path and globID "-" represents reading from the stdin pipe.
	r.read(ctx, ltx, "", nil, "-", re)
}

func (r *readCommand) readJournal(ctx context.Context, ltx lcontext.LContext,
	spec string, re regex.Regex, _ int) {

	r.readFiles(ctx, ltx, []string{spec}, spec, re, r.server.ReadGlobRetryInterval())
}

func (r *readCommand) readGlob(ctx context.Context, ltx lcontext.LContext,
	glob string, re regex.Regex, retries int) {

	retryInterval := r.server.ReadGlobRetryInterval()
	glob = filepath.Clean(glob)

	for retryCount := 0; retryCount < retries; retryCount++ {
		paths, err := filepath.Glob(glob)
		if err != nil {
			dlog.Server.Warn(r.server.LogContext(), glob, err)
			if !ctxutil.Sleep(ctx, retryInterval) {
				return
			}
			continue
		}

		if numPaths := len(paths); numPaths == 0 {
			dlog.Server.Error(r.server.LogContext(), "No such file(s) to read", glob)
			r.sendServerMessage(ctx, dlog.Server.Warn(r.server.LogContext(),
				"Unable to read file(s), check server logs"))
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !ctxutil.Sleep(ctx, retryInterval) {
				return
			}
			continue
		}

		// Cap the number of paths to prevent an authenticated user with a broad
		// read permission from spawning an unbounded number of goroutines and
		// exhausting server memory. Excess paths are dropped with a warning so
		// the partial result is still delivered rather than failing entirely.
		if cap := r.server.MaxGlobTargets(); len(paths) > cap {
			dlog.Server.Warn(r.server.LogContext(), "Glob expansion exceeded cap, truncating",
				"glob", glob, "matched", len(paths), "cap", cap)
			r.sendServerMessage(ctx, dlog.Server.Warn(r.server.LogContext(),
				"Glob expansion exceeded server limit, only first targets served",
				"limit", cap, "matched", len(paths)))
			paths = paths[:cap]
		}

		r.readFiles(ctx, ltx, paths, glob, re, retryInterval)
		return
	}

	r.sendServerMessage(ctx, dlog.Server.Warn(r.server.LogContext(),
		"Giving up to read file(s)"))
	return
}

func (r *readCommand) readFiles(ctx context.Context, ltx lcontext.LContext,
	paths []string, glob string, re regex.Regex, retryInterval time.Duration) {

	dlog.Server.Info(r.server.LogContext(), "Processing files", "count", len(paths), "glob", glob)

	// Track pending files for this batch
	totalPending := r.server.AddPendingFiles(int32(len(paths)))
	dlog.Server.Info(r.server.LogContext(), "Added pending files", "count", len(paths), "totalPending", totalPending)

	var wg sync.WaitGroup
	wg.Add(len(paths))
	for _, path := range paths {
		go r.readFileIfPermissions(ctx, ltx, &wg, path, glob, re)
	}
	wg.Wait()

	dlog.Server.Info(r.server.LogContext(), "All files processed", "count", len(paths))

	select {
	case <-ctx.Done():
		return
	default:
	}

	// In output mode, signal EOF once all pending file work is drained.
	// Active command count may still include side-effect commands (for example AUTHKEY),
	// so relying on "active == 1" can skip EOF signaling and lead to dropped output.
	//
	// Output is now the only runtime path, so the former config gating on
	// the former config gating has been removed. This EOF handshake runs for every
	// cat/grep/tail read.
	//
	// The guard is the mode check rather than Aggregate() == nil:
	// SERVER-MODE dmap also enables output mode (readWithProcessor ->
	// ensureOutputEnabled) even though its lines feed the Aggregate
	// rather than the output channel writer, and it RELIES on this epilogue's
	// SignalOutputEOF to disable output mode on the session output goroutine so the
	// client receives EOF and the session terminates. Excluding the
	// output-aggregate case here (as Aggregate() == nil would) hangs
	// server-mode dmap: output mode stays enabled and the client waits forever for
	// output that never ends. dmap uses map mode (not cat/grep/tail) so it is
	// already excluded from this direct-output epilogue.
	//
	// For serverless dmap the epilogue is likewise not reached (map mode): the
	// output aggregate is finalized through FinishInput in the shutdown
	// coordinator, independently of this direct-output handshake.
	if r.mode == omode.CatClient || r.mode == omode.GrepClient || r.mode == omode.TailClient {
		if r.server.DirectOutputActive() && r.server.HasOutputEOF() {
			// Capture the handshake epoch BEFORE the pending check. A command
			// that joins the session afterwards bumps the epoch (readFiles
			// increments the pending count before its per-file work enables
			// output mode), so a joiner invisible to the pending==0 check below
			// is guaranteed to advance the epoch after this capture — turning
			// our SignalOutputEOF into a no-op instead of cutting off the
			// joiner's output mid-batch. The window between the checks below
			// and the signal is wide (FlushOutput may block for seconds),
			// which is exactly what this guard covers.
			//
			// Note the never-signaling-joiner class: a output-aggregate (dmap)
			// command joining the live session also bumps the epoch via its
			// enable, but map mode is excluded from this epilogue and never
			// signals EOF itself. Our signal is then dropped and the ack wait
			// below runs into its bounded timeout — a spurious warning, but no
			// data loss: our data was flushed above and session shutdown
			// flushes the rest (see outputManager.signalEOF).
			epoch := r.server.OutputEpoch()

			pending, active := r.server.PendingAndActive()
			shouldSignalEOF := pending == 0
			if !shouldSignalEOF {
				dlog.Server.Trace(r.server.LogContext(), "Skipping output EOF signal for non-final command",
					"pending", pending, "active", active)
				return
			}

			dlog.Server.Debug(r.server.LogContext(), "Output mode: flushing data before EOF signal")

			// Ensure all output data is flushed before signaling EOF.
			r.server.FlushOutput()

			// Signal EOF by closing the channel, but only once — and only if
			// no newer batch joined since the epoch capture above.
			r.server.SignalOutputEOF(epoch)

			// Wait for an explicit reader acknowledgement instead of timing guesses.
			if !r.server.Serverless() {
				timeout := r.server.OutputEOFAckTimeout()
				if r.server.WaitForOutputEOFAck(timeout) {
					// The wait is also released when enable() hands the
					// handshake over to a new batch (stale refresh), not only
					// by a reader ack — the log wording covers both.
					dlog.Server.Debug(r.server.LogContext(), "Output EOF handshake released (reader ack or handover)")
					// Allow transport buffers to flush after acknowledgement.
					if !ctxutil.Sleep(ctx, r.server.ShutdownSerializeWait()) {
						return
					}
				} else {
					dlog.Server.Warn(
						r.server.LogContext(),
						"Timeout waiting for output EOF acknowledgement",
						"timeout", timeout,
						"remaining", r.server.OutputChannelLen(),
					)
				}
			}
		}
	}

	// In output mode with aggregate, we don't close the shared channel here
	// because it will be used across multiple invocations
	// The aggregate will handle channel closure when it's done
}

func (r *readCommand) readFileIfPermissions(ctx context.Context, ltx lcontext.LContext,
	wg *sync.WaitGroup, path, glob string, re regex.Regex) {

	defer wg.Done()
	defer func() {
		r.shutdownCoordinator.onFileProcessed(path)
	}()

	globID := r.makeGlobID(ctx, path, glob)
	target, ok := r.server.PrepareReadTarget(path)
	if !ok {
		dlog.Server.Error(r.server.LogContext(), "No permission to read file", path, globID)
		r.sendServerMessage(ctx, dlog.Server.Warn(r.server.LogContext(),
			"Unable to read file(s), check server logs"))
		return
	}
	r.read(ctx, ltx, path, &target, globID, re)
}

func (r *readCommand) read(ctx context.Context, ltx lcontext.LContext,
	path string, target *fs.ValidatedReadTarget, globID string, re regex.Regex) {

	dlog.Server.Info(r.server.LogContext(), "Start reading", path, globID)
	r.logRegexMode(re)

	var reader fs.FileReader
	var limiter chan struct{}
	serverMessages, closeServerMessages := r.newGeneratedServerMessagesChannel(ctx)
	defer closeServerMessages()

	switch r.mode {
	case omode.GrepClient, omode.CatClient:
		if target != nil && target.Kind == fs.JournalKind {
			journalReader, err := journal.NewReader(journalArgs(path), path, false, serverMessages)
			if err != nil {
				r.sendServerMessage(ctx, dlog.Server.Warn(r.server.LogContext(), "Unable to read journal", err))
				return
			}
			reader = journalReader
		} else if target != nil {
			catFile := fs.NewValidatedCatFile(path, *target, globID, serverMessages, r.server.MaxLineLength())
			reader = &catFile
		} else {
			catFile := fs.NewCatFile(path, globID, serverMessages, r.server.MaxLineLength())
			reader = &catFile
		}
		limiter = r.server.CatLimiter()
	case omode.TailClient:
		fallthrough
	default:
		if target != nil && target.Kind == fs.JournalKind {
			journalReader, err := journal.NewReader(journalArgs(path), path, true, serverMessages)
			if err != nil {
				r.sendServerMessage(ctx, dlog.Server.Warn(r.server.LogContext(), "Unable to read journal", err))
				return
			}
			reader = journalReader
		} else if target != nil {
			tailFile := fs.NewValidatedTailFile(path, *target, globID, serverMessages, r.server.MaxLineLength())
			reader = &tailFile
		} else {
			tailFile := fs.NewTailFile(path, globID, serverMessages, r.server.MaxLineLength())
			reader = &tailFile
		}
		limiter = r.server.TailLimiter()
	}

	// acquired tracks whether this goroutine successfully sent to the limiter.
	// The defer must only release a slot when this goroutine actually holds one;
	// an unconditional release would steal a slot from another goroutine that
	// is still holding it, permanently reducing the effective semaphore capacity.
	var acquired bool
	defer func() {
		if acquired {
			<-limiter
		}
	}()

	select {
	case limiter <- struct{}{}:
		acquired = true
		dlog.Server.Debug(r.server.LogContext(), "Got limiter slot immediately", "path", path)
	case <-ctx.Done():
		dlog.Server.Debug(r.server.LogContext(), "Context cancelled while waiting for limiter", "path", path)
		return
	default:
		dlog.Server.Info(r.server.LogContext(), "Server limit hit, queueing file", "limiterLen", len(limiter), "path", path, "maxConcurrent", cap(limiter))
		select {
		case limiter <- struct{}{}:
			acquired = true
			dlog.Server.Info(r.server.LogContext(), "Server limit OK now, processing file", "limiterLen", len(limiter), "path", path)
		case <-ctx.Done():
			dlog.Server.Debug(r.server.LogContext(), "Context cancelled while queued for limiter", "path", path)
			return
		}
	}

	// Output is the one and only read path. read() is only ever invoked for the
	// cat/grep/tail command handlers (see makeReadCommandHandler), and MapReduce
	// always builds a Aggregate for both server mode and serverless (see
	// mapcommand.go). So either the read runs in cat/grep mode with
	// Aggregate() non-nil and feeds it directly via AggregateProcessor,
	// or it is genuine cat/grep/tail output using the output direct-output writer;
	// makeProcessor picks between the two. The former channel-based fallback
	// (readViaChannels feeding the regular server.Aggregate) was removed once
	// serverless MapReduce migrated to the output aggregate (tasks sv0/hv0), so
	// there is no non-output path left here.
	dlog.Server.Debug(r.server.LogContext(), "Selecting read mode",
		"mode", r.mode, "hasAggregate", r.server.Aggregate() != nil)
	dlog.Server.Info(r.server.LogContext(), "Using turbo mode for reading", path, "mode", r.mode, "hasAggregate", r.server.Aggregate() != nil)
	r.readWithProcessor(ctx, ltx, path, globID, re, reader)
}

func journalArgs(spec string) []string {
	source := strings.TrimPrefix(spec, fs.JournalSpecPrefix)
	if source == "" {
		return nil
	}
	return []string{"-u", source}
}

func (r *readCommand) readWithProcessor(ctx context.Context, ltx lcontext.LContext,
	path, globID string, re regex.Regex, reader fs.FileReader) {

	dlog.Server.Info(r.server.LogContext(), "Using output channel-less implementation", path, globID)
	r.logRegexMode(re)

	r.ensureOutputEnabled(ctx)
	writer := r.makeWriter(ctx)

	r.executeReadLoop(ctx, ltx, path, globID, re, reader, r.readViaProcessor(path, globID, writer))
}

func (r *readCommand) executeReadLoop(ctx context.Context, ltx lcontext.LContext,
	path, globID string, re regex.Regex, reader fs.FileReader, strategy readStrategy) {

	for {
		if err := strategy(ctx, ltx, reader, re); err != nil {
			dlog.Server.Error(r.server.LogContext(), path, globID, err)
		}

		select {
		case <-ctx.Done():
			return
		default:
			if !reader.Retry() {
				return
			}
		}

		if !ctxutil.Sleep(ctx, r.server.ReadRetryInterval()) {
			return
		}
		dlog.Server.Info(path, globID, "Reading file again")
	}
}

func (r *readCommand) readViaProcessor(path, globID string, writer LineWriter) readStrategy {
	return func(ctx context.Context, ltx lcontext.LContext, reader fs.FileReader, re regex.Regex) error {
		dlog.Server.Trace(r.server.LogContext(), path, globID, "readWithProcessor -> starting read loop iteration")

		processor := r.makeProcessor(path, globID, writer)

		dlog.Server.Trace(r.server.LogContext(), path, globID, "readWithProcessor -> reader.StartWithPocessorOptimized -> about to start")
		startErr := reader.StartWithProcessorOptimized(ctx, ltx, processor, re)
		dlog.Server.Trace(r.server.LogContext(), path, globID, "readWithProcessor -> reader.StartWithPocessorOptimized -> completed")

		// Ensure we flush and close the processor before retry checks.
		dlog.Server.Trace(r.server.LogContext(), path, globID, "readWithProcessor -> flushing processor")
		if flushErr := processor.Flush(); flushErr != nil {
			dlog.Server.Error(r.server.LogContext(), path, globID, "flush error", flushErr)
		}
		dlog.Server.Trace(r.server.LogContext(), path, globID, "readWithProcessor -> closing processor")
		if closeErr := processor.Close(); closeErr != nil {
			dlog.Server.Error(r.server.LogContext(), path, globID, "close error", closeErr)
		}
		dlog.Server.Trace(r.server.LogContext(), path, globID, "readWithProcessor -> processor closed")

		// Give time for data to be transmitted.
		// This is crucial for integration tests to ensure all data is sent
		// Skip this delay in serverless mode since data is written directly to stdout
		if !r.server.Serverless() {
			dlog.Server.Trace(r.server.LogContext(), path, globID, "readWithProcessor -> waiting for data transmission")
			if !ctxutil.Sleep(ctx, r.server.OutputTransmissionDelay()) {
				return startErr
			}
		}

		return startErr
	}
}

func (r *readCommand) ensureOutputEnabled(ctx context.Context) {
	// EnableDirectOutput is an atomic check-and-enable guarded by the output
	// manager's mutex, so mode and channel initialization are always observed
	// together — no visibility double-check is needed here. It returns false
	// when output mode was already active.
	if !r.server.EnableDirectOutput() {
		return
	}
	// Wake a potentially blocked reader goroutine so it can switch to output drain path.
	r.sendServerMessage(ctx, ".output wake")
}

func (r *readCommand) makeWriter(ctx context.Context) LineWriter {
	// Create a writer instance per file to keep concurrent processing isolated.
	if r.server.Serverless() {
		return NewGeneratedDirectWriter(serverlessOutputWriter(), r.server.Hostname(), r.server.PlainOutput(), r.server.Serverless(), r.generation, r.server.ActiveSessionGeneration)
	}

	// Use NewNetworkWriter so bufSize is set to 64KB. A bare struct literal
	// here previously left bufSize at zero, which disabled write batching and
	// sent every line as its own output-channel payload (one SSH packet + one
	// write syscall per line), making server-mode output output far slower than
	// it should be.
	return NewNetworkWriter(ctx, r.server.GetOutputChannel(),
		r.server.ServerMessagesChannel(), r.server.Hostname(),
		r.server.PlainOutput(), r.server.Serverless(), r.generation,
		r.server.ActiveSessionGeneration)
}

// serverlessOutputWriter returns the io.Writer the serverless direct-output
// (output) path writes payload to. It is os.Stdout by default. When the client
// opted in via --log-payload / Client.LogPayload, it additionally tees the exact
// same payload bytes into the fout daily log FILE sink.
//
// This is needed because output is now the only runtime path: the serverless
// direct-output path writes payload straight to stdout and bypasses the fout
// logger's Raw method, so the logger's own --log-payload file tee never runs.
// The tee is added via io.MultiWriter, which writes to os.Stdout first and byte
// for byte unchanged, so stdout stays identical whether or not --log-payload is
// set; only the file gains the payload. When LogPayload is off (the default) we
// return the bare os.Stdout so the hot path stays allocation-free.
func serverlessOutputWriter() io.Writer {
	if config.Client != nil && config.Client.LogPayload {
		return io.MultiWriter(os.Stdout, payloadFileTeeWriter{})
	}
	return os.Stdout
}

// payloadFileTeeWriter is an io.Writer that forwards serverless payload bytes to
// the client logger's FILE sink only (never stdout), honoring --log-payload. It
// lets the serverless direct-output path reuse the fout daily-log file tee that
// the bypassed logger.Raw path would otherwise have provided.
type payloadFileTeeWriter struct{}

func (payloadFileTeeWriter) Write(p []byte) (int, error) {
	// dlog.Client is the fout logger that owns the daily log file in serverless
	// mode; RawPayloadFileTee no-ops when the logger has no file sink or payload
	// teeing is disabled. Report the full length as written so io.MultiWriter
	// does not treat the tee as a short write.
	if dlog.Client != nil {
		dlog.Client.RawPayloadFileTee(string(p))
	}
	return len(p), nil
}

func (r *readCommand) makeProcessor(path, globID string, writer LineWriter) readProcessor {
	if aggregate := r.server.Aggregate(); aggregate != nil {
		dlog.Server.Info(r.server.LogContext(), "Using turbo aggregate processor for MapReduce", path, globID)
		return server.NewAggregateProcessor(aggregate, globID)
	}

	return NewDirectLineProcessor(writer, globID)
}

func (r *readCommand) logRegexMode(re regex.Regex) {
	if r.mode != omode.GrepClient {
		return
	}
	if re.IsLiteral() {
		dlog.Server.Info(r.server.LogContext(), "Using optimized literal string matching for pattern:", re.Pattern())
	} else {
		dlog.Server.Info(r.server.LogContext(), "Using regex matching for pattern:", re.Pattern())
	}
}

func (r *readCommand) makeGlobID(ctx context.Context, path, glob string) string {
	var idParts []string
	pathParts := strings.Split(path, "/")

	for i, globPart := range strings.Split(glob, "/") {
		if strings.Contains(globPart, "*") {
			idParts = append(idParts, pathParts[i])
		}
	}

	if len(idParts) > 0 {
		return strings.Join(idParts, "/")
	}
	if len(pathParts) > 0 {
		return pathParts[len(pathParts)-1]
	}

	r.sendServerMessage(ctx, dlog.Server.Warn("Empty file path given?", path, glob))
	return ""
}

// sendServerMessage forwards a user-visible message to the session's shared
// serverMessages channel (capacity 10, drained only by baseHandler.Read). A
// bare send here would block forever once the client disconnects and Read
// stops draining — pinning one goroutine per stuck send (e.g. one per
// permission-denied file of a glob expansion, up to MaxGlobTargets). The
// select on ctx.Done() makes the send abandonable: the per-command context is
// cancelled on command completion and on handler shutdown (see
// baseHandler.newCommandContext), mirroring the done-guarded baseHandler.send.
func (r *readCommand) sendServerMessage(ctx context.Context, message string) {
	select {
	case r.server.ServerMessagesChannel() <- encodeGeneratedMessage(r.generation, message+"\n"):
	case <-ctx.Done():
	}
}

func (r *readCommand) newGeneratedServerMessagesChannel(ctx context.Context) (chan string, func()) {
	serverMessages := make(chan string, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case message, ok := <-serverMessages:
				if !ok {
					return
				}
				select {
				case r.server.ServerMessagesChannel() <- encodeGeneratedMessage(r.generation, message):
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return serverMessages, func() {
		close(serverMessages)
		<-done
	}
}

func (r *readCommand) isInputFromPipe() bool {
	if !r.server.Serverless() {
		// Can read from pipe only in serverless mode.
		return false
	}
	fileInfo, _ := os.Stdin.Stat()
	return fileInfo.Mode()&os.ModeCharDevice == 0
}
