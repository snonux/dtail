package handlers

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mimecast/dtail/internal/ctxutil"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/journal"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

type readCommand struct {
	server               readCommandServer
	aggregate            *server.Aggregate
	mode                 omode.Mode
	generation           uint64
	pendingInputReserved bool
	shutdownCoordinator  *shutdownCoordinator
	inputBatch           *commandBatch
	inputBatchRead       commandBatchRead
}

type pendingInputReservationKeyType struct{}

var pendingInputReservationKey pendingInputReservationKeyType

// pendingInputReservation bridges synchronous protocol dispatch and the
// asynchronously started readCommand. claim and releaseIfUnclaimed are called
// by the dispatch goroutine before the handler returns; the read goroutine owns
// the pending counter only after claim succeeds.
type pendingInputReservation struct {
	server              readCommandServer
	aggregate           *server.Aggregate
	shutdownCoordinator *shutdownCoordinator
	admission           *commandAdmissionResult
	inputBatch          *commandBatch
	inputBatchRead      commandBatchRead
	claimed             bool
	released            bool
}

func newPendingInputReservation(server readCommandServer, mode omode.Mode) *pendingInputReservation {
	server.AddPendingFiles(1)
	aggregate := server.Aggregate()
	oneShotInput := mode == omode.CatClient || mode == omode.GrepClient
	return &pendingInputReservation{
		server:              server,
		aggregate:           aggregate,
		shutdownCoordinator: newShutdownCoordinator(server, oneShotInput, aggregate),
	}
}

func withPendingInputReservation(ctx context.Context, reservation *pendingInputReservation) context.Context {
	return context.WithValue(ctx, pendingInputReservationKey, reservation)
}

func pendingInputReservationFromContext(ctx context.Context) *pendingInputReservation {
	reservation, _ := ctx.Value(pendingInputReservationKey).(*pendingInputReservation)
	return reservation
}

func (r *pendingInputReservation) claim() bool {
	if r == nil || r.claimed || r.released {
		return false
	}
	r.claimed = true
	return true
}

func (r *pendingInputReservation) releaseIfUnclaimed() {
	if r == nil || r.claimed || r.released {
		return
	}
	r.released = true
	if r.admission != nil && r.admission.admitted {
		// The admitted command already ran its commandFinished callback while
		// this reservation kept pending input non-zero. Complete through the
		// normal read lifecycle so the final zero transition can both finish a
		// one-shot aggregate and start idle shutdown.
		r.shutdownCoordinator.onFileProcessed("unclaimed read command")
		r.completeInputBatch()
		return
	}

	remaining, _ := r.server.CompletePendingFile()
	if remaining == 0 {
		// Dispatch can reject the command before admission (for example during
		// graceful shutdown or option parsing). Preserve aggregate completion
		// for an older read without starting a competing shutdown sequence.
		r.shutdownCoordinator.maybeFinishAggregateInput()
	}
	r.completeInputBatch()
}

func (r *pendingInputReservation) completeInputBatch() {
	if r.inputBatch == nil || !r.inputBatchRead.tracked {
		return
	}
	if aggregate := r.inputBatch.completeRead(r.inputBatchRead); aggregate != nil {
		aggregate.FinishInput()
	}
	r.inputBatch = nil
	r.inputBatchRead = commandBatchRead{}
}

type readStrategy func(context.Context, lcontext.LContext, fs.FileReader, regex.Regex) error

type readProcessor interface {
	ProcessLine(*bytes.Buffer, uint64, string) error
	Flush() error
	Close() error
}

func newReadCommand(server readCommandServer, mode omode.Mode) *readCommand {
	return newReadCommandWithAggregate(server, mode, server.Aggregate())
}

func newReadCommandWithAggregate(server readCommandServer, mode omode.Mode, aggregate *server.Aggregate) *readCommand {
	// cat/grep reads are one-shot: their input is exhausted once every file
	// has been read to EOF. tail follows its files indefinitely, so its input
	// never exhausts and must not finish a output aggregate.
	oneShotInput := mode == omode.CatClient || mode == omode.GrepClient
	return &readCommand{
		server:              server,
		aggregate:           aggregate,
		mode:                mode,
		shutdownCoordinator: newShutdownCoordinator(server, oneShotInput, aggregate),
	}
}

func (r *readCommand) Start(ctx context.Context, ltx lcontext.LContext,
	argc int, args []string, retries int) {
	// If parsing, glob expansion, retry cancellation, or another early-return
	// path prevents this command from resolving concrete inputs, release its
	// dispatch-time reservation here. Once an input is resolved, readPipe or
	// readFiles transfers the reservation and this becomes a no-op.
	defer r.releasePendingInputReservation()
	r.generation = sessionGenerationFromContext(ctx)

	re := regex.NewNoop()
	if argc >= 4 {
		deserializedRegex, err := regex.Deserialize(strings.Join(args[2:], " "))
		if err != nil {
			r.sendServerMessage(ctx, r.server.Logger().Error(r.server.LogContext(),
				"Unable to parse command", err))
			return
		}
		re = deserializedRegex
	}
	if argc < 3 {
		r.sendServerMessage(ctx, r.server.Logger().Warn(r.server.LogContext(),
			"Unable to parse command", args, argc))
		return
	}

	// In serverless mode, can also read data from pipe
	// e.g.: grep foo bar.log | dmap 'from STATS select ...'
	// Only read from pipe if no file argument is provided
	isPipe := r.isInputFromPipe() && (argc < 2 || args[1] == "" || args[1] == "-")
	switch {
	case isPipe:
		r.server.Logger().Debug("Reading data from stdin pipe")
		r.readPipe(ctx, ltx, re)
	case fs.IsJournalSpec(args[1]):
		r.server.Logger().Debug("Reading data from journal")
		r.readJournal(ctx, ltx, args[1], re)
	default:
		r.server.Logger().Debug("Reading data from file(s)")
		r.readGlob(ctx, ltx, args[1], re, retries)
	}
}

func (r *readCommand) adoptPendingInputReservation(reservation *pendingInputReservation) {
	if reservation == nil || !reservation.claim() {
		r.server.AddPendingFiles(1)
		r.pendingInputReserved = true
		return
	}
	r.shutdownCoordinator = reservation.shutdownCoordinator
	r.inputBatch = reservation.inputBatch
	r.inputBatchRead = reservation.inputBatchRead
	r.pendingInputReserved = true
}

func (r *readCommand) completeInputBatch() {
	if r.inputBatch == nil || !r.inputBatchRead.tracked {
		return
	}
	if aggregate := r.inputBatch.completeRead(r.inputBatchRead); aggregate != nil {
		aggregate.FinishInput()
	}
	r.inputBatch = nil
	r.inputBatchRead = commandBatchRead{}
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
// equivalent "input exhausted" signal. registerPendingFiles transfers the
// dispatch-time reservation to the pipe (or creates one for direct unit use),
// balancing the unconditional decrement in onFileProcessed.
func (r *readCommand) readPipe(ctx context.Context, ltx lcontext.LContext, re regex.Regex) {
	r.registerPendingFiles(1)
	defer r.shutdownCoordinator.onFileProcessed("-")
	// Empty file path and globID "-" represents reading from the stdin pipe.
	r.read(ctx, ltx, "", nil, "-", re)
}

func (r *readCommand) readJournal(ctx context.Context, ltx lcontext.LContext,
	spec string, re regex.Regex) {

	r.readFiles(ctx, ltx, []string{spec}, spec, re)
}

func (r *readCommand) readGlob(ctx context.Context, ltx lcontext.LContext,
	glob string, re regex.Regex, retries int) {

	retryInterval := r.server.ReadGlobRetryInterval()
	glob = filepath.Clean(glob)

	for retryCount := 0; retryCount < retries; retryCount++ {
		paths, err := filepath.Glob(glob)
		if err != nil {
			r.server.Logger().Warn(r.server.LogContext(), glob, err)
			if !ctxutil.Sleep(ctx, retryInterval) {
				return
			}
			continue
		}

		if numPaths := len(paths); numPaths == 0 {
			r.server.Logger().Error(r.server.LogContext(), "No such file(s) to read", glob)
			r.sendServerMessage(ctx, r.server.Logger().Warn(r.server.LogContext(),
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
		if maxTargets := r.server.MaxGlobTargets(); len(paths) > maxTargets {
			r.server.Logger().Warn(r.server.LogContext(), "Glob expansion exceeded cap, truncating",
				"glob", glob, "matched", len(paths), "cap", maxTargets)
			r.sendServerMessage(ctx, r.server.Logger().Warn(r.server.LogContext(),
				"Glob expansion exceeded server limit, only first targets served",
				"limit", maxTargets, "matched", len(paths)))
			paths = paths[:maxTargets]
		}

		r.readFiles(ctx, ltx, paths, glob, re)
		return
	}

	r.sendServerMessage(ctx, r.server.Logger().Warn(r.server.LogContext(),
		"Giving up to read file(s)"))
}

func (r *readCommand) readFiles(ctx context.Context, ltx lcontext.LContext,
	paths []string, glob string, re regex.Regex) {

	r.server.Logger().Info(r.server.LogContext(), "Processing files", "count", len(paths), "glob", glob)

	// Transfer the dispatch-time reservation to the resolved paths before any
	// file goroutine can complete. Adding len(paths)-1 preserves a non-zero
	// count throughout the hand-off. Direct unit callers without a reservation
	// continue to register the full path count.
	totalPending := r.registerPendingFiles(len(paths))
	r.server.Logger().Info(r.server.LogContext(), "Added pending files", "count", len(paths), "totalPending", totalPending)

	var wg sync.WaitGroup
	wg.Add(len(paths))
	for _, path := range paths {
		go r.readFileIfPermissions(ctx, ltx, &wg, path, glob, re)
	}
	wg.Wait()

	r.server.Logger().Info(r.server.LogContext(), "All files processed", "count", len(paths))

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
				r.server.Logger().Trace(r.server.LogContext(), "Skipping output EOF signal for non-final command",
					"pending", pending, "active", active)
				return
			}

			r.server.Logger().Debug(r.server.LogContext(), "Output mode: flushing data before EOF signal")

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
					r.server.Logger().Debug(r.server.LogContext(), "Output EOF handshake released (reader ack or handover)")
					// Allow transport buffers to flush after acknowledgement.
					if !ctxutil.Sleep(ctx, r.server.ShutdownSerializeWait()) {
						return
					}
				} else {
					r.server.Logger().Warn(
						r.server.LogContext(),
						"Timeout waiting for output EOF acknowledgement",
						"timeout", timeout,
						"remainingBytes", r.server.OutputBufferBytes(),
					)
				}
			}
		}
	}

	// In output mode with aggregate, we don't close the shared channel here
	// because it will be used across multiple invocations
	// The aggregate will handle channel closure when it's done
}

func (r *readCommand) registerPendingFiles(count int) int32 {
	if count <= 0 {
		pending, _ := r.server.PendingAndActive()
		return pending
	}
	delta := int32(count)
	if r.pendingInputReserved {
		delta--
		r.pendingInputReserved = false
	}
	if delta != 0 {
		return r.server.AddPendingFiles(delta)
	}
	pending, _ := r.server.PendingAndActive()
	return pending
}

func (r *readCommand) releasePendingInputReservation() {
	if !r.pendingInputReserved {
		return
	}
	r.pendingInputReserved = false
	r.shutdownCoordinator.onFileProcessed("unresolved read command")
}

func (r *readCommand) readFileIfPermissions(ctx context.Context, ltx lcontext.LContext,
	wg *sync.WaitGroup, path, glob string, re regex.Regex) {

	defer recoverHandlerPanic(r.server.Logger(), r.server.LogContext(), "file read cleanup", r.abortAfterPanic)
	defer wg.Done()
	defer func() {
		r.shutdownCoordinator.onFileProcessed(path)
	}()
	defer recoverHandlerPanic(r.server.Logger(), r.server.LogContext(), "file read", r.abortAfterPanic)

	globID := r.makeGlobID(ctx, path, glob)
	target, ok := r.server.PrepareReadTarget(path)
	if !ok {
		r.server.Logger().Error(r.server.LogContext(), "No permission to read file", path, globID)
		r.sendServerMessage(ctx, r.server.Logger().Warn(r.server.LogContext(),
			"Unable to read file(s), check server logs"))
		return
	}
	r.read(ctx, ltx, path, &target, globID, re)
}

func (r *readCommand) read(ctx context.Context, ltx lcontext.LContext,
	path string, target *fs.ValidatedReadTarget, globID string, re regex.Regex) {

	r.server.Logger().Info(r.server.LogContext(), "Start reading", path, globID)
	r.logRegexMode(re)

	var reader fs.FileReader
	var limiter chan struct{}
	serverMessages, closeServerMessages := r.newGeneratedServerMessagesChannel(ctx)
	defer closeServerMessages()

	switch r.mode {
	case omode.GrepClient, omode.CatClient:
		switch {
		case target != nil && target.Kind == fs.JournalKind:
			journalReader, err := journal.NewReader(journalArgs(path), path, false, serverMessages)
			if err != nil {
				r.sendServerMessage(ctx, r.server.Logger().Warn(r.server.LogContext(), "Unable to read journal", err))
				return
			}
			reader = journalReader
		case target != nil:
			catFile := fs.NewValidatedCatFile(path, *target, globID, serverMessages,
				r.server.MaxLineLength(), r.server.ReaderLogger())
			reader = &catFile
		default:
			catFile := fs.NewCatFile(path, globID, serverMessages, r.server.MaxLineLength(), r.server.ReaderLogger())
			reader = &catFile
		}
		limiter = r.server.CatLimiter()
	case omode.TailClient:
		fallthrough
	default:
		switch {
		case target != nil && target.Kind == fs.JournalKind:
			journalReader, err := journal.NewReader(journalArgs(path), path, true, serverMessages)
			if err != nil {
				r.sendServerMessage(ctx, r.server.Logger().Warn(r.server.LogContext(), "Unable to read journal", err))
				return
			}
			reader = journalReader
		case target != nil:
			tailFile := fs.NewValidatedTailFile(path, *target, globID, serverMessages,
				r.server.MaxLineLength(), r.server.ReaderLogger())
			reader = &tailFile
		default:
			tailFile := fs.NewTailFile(path, globID, serverMessages, r.server.MaxLineLength(), r.server.ReaderLogger())
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
		r.server.Logger().Debug(r.server.LogContext(), "Got limiter slot immediately", "path", path)
	case <-ctx.Done():
		r.server.Logger().Debug(r.server.LogContext(), "Context cancelled while waiting for limiter", "path", path)
		return
	default:
		r.server.Logger().Info(r.server.LogContext(), "Server limit hit, queueing file", "limiterLen", len(limiter), "path", path, "maxConcurrent", cap(limiter))
		select {
		case limiter <- struct{}{}:
			acquired = true
			r.server.Logger().Info(r.server.LogContext(), "Server limit OK now, processing file", "limiterLen", len(limiter), "path", path)
		case <-ctx.Done():
			r.server.Logger().Debug(r.server.LogContext(), "Context cancelled while queued for limiter", "path", path)
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
	r.server.Logger().Debug(r.server.LogContext(), "Selecting read mode",
		"mode", r.mode, "hasAggregate", r.aggregate != nil)
	r.server.Logger().Info(r.server.LogContext(), "Using turbo mode for reading", path, "mode", r.mode, "hasAggregate", r.aggregate != nil)
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

	r.server.Logger().Info(r.server.LogContext(), "Using output channel-less implementation", path, globID)
	r.logRegexMode(re)

	r.ensureOutputEnabled(ctx)
	writer := r.makeWriter(ctx)

	r.executeReadLoop(ctx, ltx, path, globID, re, reader, r.readViaProcessor(path, globID, writer))
}

func (r *readCommand) executeReadLoop(ctx context.Context, ltx lcontext.LContext,
	path, globID string, re regex.Regex, reader fs.FileReader, strategy readStrategy) {

	for {
		if err := strategy(ctx, ltx, reader, re); err != nil {
			r.server.Logger().Error(r.server.LogContext(), path, globID, err)
			if errors.Is(err, fs.ErrReaderWorkerPanic) {
				panic(err)
			}
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
		r.server.Logger().Info(path, globID, "Reading file again")
	}
}

func (r *readCommand) readViaProcessor(path, globID string, writer LineWriter) readStrategy {
	return func(ctx context.Context, ltx lcontext.LContext, reader fs.FileReader, re regex.Regex) error {
		r.server.Logger().Trace(r.server.LogContext(), path, globID, "readWithProcessor -> starting read loop iteration")

		processor := r.makeProcessor(path, globID, writer)

		startErr := func() error {
			// Register cleanup before entering reader code. A panic in either the
			// reader or Flush must still close an AggregateProcessor and release
			// its processorsWg reservation. LIFO order preserves Flush-before-Close.
			defer func() {
				closeErr := processor.Close()
				if closeErr != nil {
					r.server.Logger().Error(r.server.LogContext(), path, globID, "close error", closeErr)
				}
				r.server.Logger().Trace(r.server.LogContext(), path, globID, "readWithProcessor -> processor closed")
			}()
			defer func() {
				r.server.Logger().Trace(r.server.LogContext(), path, globID, "readWithProcessor -> flushing processor")
				if flushErr := processor.Flush(); flushErr != nil {
					r.server.Logger().Error(r.server.LogContext(), path, globID, "flush error", flushErr)
				}
			}()

			r.server.Logger().Trace(r.server.LogContext(), path, globID, "readWithProcessor -> reader.StartWithPocessorOptimized -> about to start")
			err := reader.StartWithProcessorOptimized(ctx, ltx, processor, re)
			r.server.Logger().Trace(r.server.LogContext(), path, globID, "readWithProcessor -> reader.StartWithPocessorOptimized -> completed")
			return err
		}()

		// Give time for data to be transmitted.
		// This is crucial for integration tests to ensure all data is sent
		// Skip this delay in serverless mode since data is written directly to stdout
		if !r.server.Serverless() {
			r.server.Logger().Trace(r.server.LogContext(), path, globID, "readWithProcessor -> waiting for data transmission")
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
		return NewGeneratedDirectWriter(r.server.ServerlessOutput(), r.server.Hostname(), r.server.PlainOutput(), r.server.Serverless(), r.generation, r.server.ActiveSessionGeneration)
	}

	// Use NewNetworkWriter so bufSize is set to 64KB. A bare struct literal
	// here previously left bufSize at zero, which disabled write batching and
	// sent every line as its own output-channel payload (one SSH packet + one
	// write syscall per line), making server-mode output output far slower than
	// it should be.
	writer := NewNetworkWriter(ctx, nil,
		r.server.ServerMessagesChannel(), r.server.Hostname(),
		r.server.PlainOutput(), r.server.Serverless(), r.generation,
		r.server.ActiveSessionGeneration, r.server.Logger())
	writer.enqueueOutput = r.server.EnqueueOutput
	return writer
}

func (r *readCommand) makeProcessor(path, globID string, writer LineWriter) readProcessor {
	if aggregate := r.aggregate; aggregate != nil {
		r.server.Logger().Info(r.server.LogContext(), "Using turbo aggregate processor for MapReduce", path, globID)
		return server.NewAggregateProcessor(aggregate, globID)
	}

	return NewDirectLineProcessor(writer, globID, r.server.Logger())
}

func (r *readCommand) logRegexMode(re regex.Regex) {
	if r.mode != omode.GrepClient {
		return
	}
	switch {
	case re.IsLiteral():
		r.server.Logger().Info(r.server.LogContext(), "Using optimized literal string matching for pattern:", re.Pattern())
	default:
		r.server.Logger().Info(r.server.LogContext(), "Using regex matching for pattern:", re.Pattern())
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

	r.sendServerMessage(ctx, r.server.Logger().Warn("Empty file path given?", path, glob))
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
		defer recoverHandlerPanic(r.server.Logger(), r.server.LogContext(), "server message forwarding", r.abortAfterPanic)
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

func (r *readCommand) abortAfterPanic() {
	if aborter, ok := r.server.(interface{ abortAfterPanic() }); ok {
		aborter.abortAfterPanic()
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
