package handlers

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

type readCommand struct {
	server              *ServerHandler
	mode                omode.Mode
	shutdownCoordinator *shutdownCoordinator
}

type readStrategy func(context.Context, lcontext.LContext, fs.FileReader, regex.Regex) error

func newReadCommand(server *ServerHandler, mode omode.Mode) *readCommand {
	return &readCommand{
		server:              server,
		mode:                mode,
		shutdownCoordinator: newShutdownCoordinator(server),
	}
}

func (r *readCommand) Start(ctx context.Context, ltx lcontext.LContext,
	argc int, args []string, retries int) {

	re := regex.NewNoop()
	if argc >= 4 {
		deserializedRegex, err := regex.Deserialize(strings.Join(args[2:], " "))
		if err != nil {
			r.server.sendln(r.server.serverMessages, dlog.Server.Error(r.server.user,
				"Unable to parse command", err))
			return
		}
		re = deserializedRegex
	}
	if argc < 3 {
		r.server.sendln(r.server.serverMessages, dlog.Server.Warn(r.server.user,
			"Unable to parse command", args, argc))
		return
	}

	// In serverless mode, can also read data from pipe
	// e.g.: grep foo bar.log | dmap 'from STATS select ...'
	// Only read from pipe if no file argument is provided
	isPipe := r.isInputFromPipe() && (argc < 2 || args[1] == "" || args[1] == "-")

	if isPipe {
		dlog.Server.Debug("Reading data from stdin pipe")
		// Empty file path and globID "-" represents reading from the stdin pipe.
		r.read(ctx, ltx, "", "-", re)
		return
	}

	dlog.Server.Debug("Reading data from file(s)")
	r.readGlob(ctx, ltx, args[1], re, retries)
}

func (r *readCommand) readGlob(ctx context.Context, ltx lcontext.LContext,
	glob string, re regex.Regex, retries int) {

	retryInterval := time.Second * 5
	glob = filepath.Clean(glob)

	for retryCount := 0; retryCount < retries; retryCount++ {
		paths, err := filepath.Glob(glob)
		if err != nil {
			dlog.Server.Warn(r.server.user, glob, err)
			time.Sleep(retryInterval)
			continue
		}

		if numPaths := len(paths); numPaths == 0 {
			dlog.Server.Error(r.server.user, "No such file(s) to read", glob)
			r.server.sendln(r.server.serverMessages, dlog.Server.Warn(r.server.user,
				"Unable to read file(s), check server logs"))
			select {
			case <-ctx.Done():
				return
			default:
			}
			time.Sleep(retryInterval)
			continue
		}

		r.readFiles(ctx, ltx, paths, glob, re, retryInterval)
		return
	}

	r.server.sendln(r.server.serverMessages, dlog.Server.Warn(r.server.user,
		"Giving up to read file(s)"))
	return
}

func (r *readCommand) readFiles(ctx context.Context, ltx lcontext.LContext,
	paths []string, glob string, re regex.Regex, retryInterval time.Duration) {

	dlog.Server.Info(r.server.user, "Processing files", "count", len(paths), "glob", glob)

	// Track pending files for this batch
	atomic.AddInt32(&r.server.pendingFiles, int32(len(paths)))
	dlog.Server.Info(r.server.user, "Added pending files", "count", len(paths), "totalPending", atomic.LoadInt32(&r.server.pendingFiles))

	var wg sync.WaitGroup
	wg.Add(len(paths))
	for _, path := range paths {
		go r.readFileIfPermissions(ctx, ltx, &wg, path, glob, re)
	}
	wg.Wait()

	dlog.Server.Info(r.server.user, "All files processed", "count", len(paths))

	// In turbo mode, signal EOF after all files are processed
	// This is crucial for proper shutdown in server mode
	if !r.server.serverCfg.TurboBoostDisable && r.server.aggregate == nil &&
		(r.mode == omode.CatClient || r.mode == omode.GrepClient || r.mode == omode.TailClient) {
		if r.server.IsTurboMode() && r.server.HasTurboEOF() {
			dlog.Server.Debug(r.server.user, "Turbo mode: flushing data before EOF signal")

			// Ensure all turbo data is flushed before signaling EOF
			r.server.flushTurboData()

			// Signal EOF by closing the channel, but only once.
			r.server.SignalTurboEOF()

			// Wait to ensure all data is transmitted
			// This is especially important when files are queued due to concurrency limits
			// In serverless mode, data is written directly to stdout, so no wait is needed
			if !r.server.serverless {
				waitTime := 500 * time.Millisecond
				if len(paths) > 10 {
					// For many files, wait proportionally longer
					waitTime = time.Duration(len(paths)*10) * time.Millisecond
					if waitTime > 2*time.Second {
						waitTime = 2 * time.Second
					}
				}
				dlog.Server.Debug(r.server.user, "Waiting for data transmission", "duration", waitTime)
				time.Sleep(waitTime)
			}
		}
	}

	// In turbo mode with aggregate, we don't close the shared channel here
	// because it will be used across multiple invocations
	// The aggregate will handle channel closure when it's done
}

func (r *readCommand) readFileIfPermissions(ctx context.Context, ltx lcontext.LContext,
	wg *sync.WaitGroup, path, glob string, re regex.Regex) {

	defer wg.Done()
	defer func() {
		r.shutdownCoordinator.onFileProcessed(path)
	}()

	globID := r.makeGlobID(path, glob)
	if !r.server.user.HasFilePermission(path, "readfiles") {
		dlog.Server.Error(r.server.user, "No permission to read file", path, globID)
		r.server.sendln(r.server.serverMessages, dlog.Server.Warn(r.server.user,
			"Unable to read file(s), check server logs"))
		return
	}
	r.read(ctx, ltx, path, globID, re)
}

func (r *readCommand) read(ctx context.Context, ltx lcontext.LContext,
	path, globID string, re regex.Regex) {

	dlog.Server.Info(r.server.user, "Start reading", path, globID)
	r.logRegexMode(re)

	var reader fs.FileReader
	var limiter chan struct{}

	switch r.mode {
	case omode.GrepClient, omode.CatClient:
		catFile := fs.NewCatFile(path, globID, r.server.serverMessages)
		reader = &catFile
		limiter = r.server.catLimiter
	case omode.TailClient:
		fallthrough
	default:
		tailFile := fs.NewTailFile(path, globID, r.server.serverMessages)
		reader = &tailFile
		limiter = r.server.tailLimiter
	}

	defer func() {
		select {
		case <-limiter:
		default:
		}
	}()

	select {
	case limiter <- struct{}{}:
		dlog.Server.Debug(r.server.user, "Got limiter slot immediately", "path", path)
	case <-ctx.Done():
		dlog.Server.Debug(r.server.user, "Context cancelled while waiting for limiter", "path", path)
		return
	default:
		dlog.Server.Info(r.server.user, "Server limit hit, queueing file", "limiterLen", len(limiter), "path", path, "maxConcurrent", cap(limiter))
		select {
		case limiter <- struct{}{}:
			dlog.Server.Info(r.server.user, "Server limit OK now, processing file", "limiterLen", len(limiter), "path", path)
		case <-ctx.Done():
			dlog.Server.Debug(r.server.user, "Context cancelled while queued for limiter", "path", path)
			return
		}
	}

	// Check if we should use the turbo boost optimizations
	// Enable turbo boost for cat/grep/tail modes, and now also for MapReduce operations
	// MapReduce now has a turbo mode implementation that bypasses channels
	dlog.Server.Debug(r.server.user, "Checking turbo mode", "turboBoostDisable", r.server.serverCfg.TurboBoostDisable,
		"mode", r.mode, "hasTurboAggregate", r.server.turboAggregate != nil, "hasAggregate", r.server.aggregate != nil)
	// Only use turbo mode if:
	// 1. Turbo boost is NOT disabled (it's enabled by default) AND
	// 2. We have a turbo aggregate OR (we're in cat/grep/tail mode AND we don't have a regular aggregate)
	if !r.server.serverCfg.TurboBoostDisable &&
		(r.server.turboAggregate != nil || ((r.mode == omode.CatClient || r.mode == omode.GrepClient || r.mode == omode.TailClient) && r.server.aggregate == nil)) {
		dlog.Server.Info(r.server.user, "Using turbo mode for reading", path, "mode", r.mode, "hasTurboAggregate", r.server.turboAggregate != nil)
		r.readWithTurboProcessor(ctx, ltx, path, globID, re, reader)
		return
	}

	r.executeReadLoop(ctx, ltx, path, globID, re, reader, r.readViaChannels())
}

func (r *readCommand) readWithTurboProcessor(ctx context.Context, ltx lcontext.LContext,
	path, globID string, re regex.Regex, reader fs.FileReader) {

	dlog.Server.Info(r.server.user, "Using turbo channel-less implementation", path, globID)
	r.logRegexMode(re)

	// Enable turbo mode if not already enabled
	if !r.server.IsTurboMode() {
		r.server.EnableTurboMode()
	}

	// Create a direct writer based on the mode
	// Each file gets its own writer instance to avoid race conditions
	// when multiple files are processed concurrently
	var writer TurboWriter
	if r.server.serverless {
		// In serverless mode, write directly to stdout
		writer = NewDirectTurboWriter(os.Stdout, r.server.hostname, r.server.plain, r.server.serverless)
	} else {
		// In server mode, use the network writer with turbo channels
		// Create a new instance for each file to ensure thread safety
		writer = &TurboNetworkWriter{
			handler:    &r.server.baseHandler,
			hostname:   r.server.hostname,
			plain:      r.server.plain,
			serverless: r.server.serverless,
		}
	}

	r.executeReadLoop(ctx, ltx, path, globID, re, reader, r.readViaTurboProcessor(path, globID, writer))
}

func (r *readCommand) executeReadLoop(ctx context.Context, ltx lcontext.LContext,
	path, globID string, re regex.Regex, reader fs.FileReader, strategy readStrategy) {

	for {
		if err := strategy(ctx, ltx, reader, re); err != nil {
			dlog.Server.Error(r.server.user, path, globID, err)
		}

		select {
		case <-ctx.Done():
			return
		default:
			if !reader.Retry() {
				return
			}
		}

		time.Sleep(time.Second * 2)
		dlog.Server.Info(path, globID, "Reading file again")
	}
}

func (r *readCommand) readViaChannels() readStrategy {
	return func(ctx context.Context, ltx lcontext.LContext, reader fs.FileReader, re regex.Regex) error {
		aggregate := r.server.aggregate
		var linesCh chan *line.Line

		if aggregate != nil {
			// For MapReduce operations, create a new channel that goes only to the aggregate.
			linesCh = make(chan *line.Line, 10000)
			aggregate.NextLinesCh <- linesCh
		} else {
			// For non-MapReduce operations, use the server's shared lines channel.
			linesCh = r.server.lines
		}

		err := reader.Start(ctx, ltx, linesCh, re)
		if aggregate != nil {
			// Closing the aggregate line channel triggers flush.
			close(linesCh)
		}

		return err
	}
}

func (r *readCommand) readViaTurboProcessor(path, globID string, writer TurboWriter) readStrategy {
	return func(ctx context.Context, ltx lcontext.LContext, reader fs.FileReader, re regex.Regex) error {
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> starting read loop iteration")

		var processor interface {
			ProcessLine(*bytes.Buffer, uint64, string) error
			Flush() error
			Close() error
		}

		if r.server.turboAggregate != nil {
			// Use turbo aggregate processor for MapReduce operations.
			dlog.Server.Info(r.server.user, "Using turbo aggregate processor for MapReduce", path, globID)
			processor = server.NewTurboAggregateProcessor(r.server.turboAggregate, globID)
		} else {
			// Use direct line processor for cat/grep/tail.
			processor = NewDirectLineProcessor(writer, globID)
		}

		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> reader.StartWithPocessorOptimized -> about to start")
		startErr := reader.StartWithProcessorOptimized(ctx, ltx, processor, re)
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> reader.StartWithPocessorOptimized -> completed")

		// Ensure we flush and close the processor before retry checks.
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> flushing processor")
		if flushErr := processor.Flush(); flushErr != nil {
			dlog.Server.Error(r.server.user, path, globID, "flush error", flushErr)
		}
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> closing processor")
		if closeErr := processor.Close(); closeErr != nil {
			dlog.Server.Error(r.server.user, path, globID, "close error", closeErr)
		}
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> processor closed")

		// Give time for data to be transmitted.
		// This is crucial for integration tests to ensure all data is sent
		// Skip this delay in serverless mode since data is written directly to stdout
		if !r.server.serverless {
			dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> waiting for data transmission")
			time.Sleep(50 * time.Millisecond)
		}

		return startErr
	}
}

func (r *readCommand) logRegexMode(re regex.Regex) {
	if r.mode != omode.GrepClient {
		return
	}
	if re.IsLiteral() {
		dlog.Server.Info(r.server.user, "Using optimized literal string matching for pattern:", re.Pattern())
	} else {
		dlog.Server.Info(r.server.user, "Using regex matching for pattern:", re.Pattern())
	}
}

func (r *readCommand) makeGlobID(path, glob string) string {
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

	r.server.sendln(r.server.serverMessages,
		dlog.Server.Warn("Empty file path given?", path, glob))
	return ""
}

func (r *readCommand) isInputFromPipe() bool {
	if !r.server.serverless {
		// Can read from pipe only in serverless mode.
		return false
	}
	fileInfo, _ := os.Stdin.Stat()
	return fileInfo.Mode()&os.ModeCharDevice == 0
}
