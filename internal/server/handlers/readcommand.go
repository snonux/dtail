package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

type readCommand struct {
	server *ServerHandler
	mode   omode.Mode
}

func newReadCommand(server *ServerHandler, mode omode.Mode) *readCommand {
	return &readCommand{
		server: server,
		mode:   mode,
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

	var wg sync.WaitGroup
	wg.Add(len(paths))
	for _, path := range paths {
		go r.readFileIfPermissions(ctx, ltx, &wg, path, glob, re)
	}
	wg.Wait()

	// In turbo mode with aggregate, we don't close the shared channel here
	// because it will be used across multiple invocations
	// The aggregate will handle channel closure when it's done
}

func (r *readCommand) readFileIfPermissions(ctx context.Context, ltx lcontext.LContext,
	wg *sync.WaitGroup, path, glob string, re regex.Regex) {

	defer wg.Done()
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
	case <-ctx.Done():
		return
	default:
		dlog.Server.Info("Server limit hit, queueing file", len(limiter), path)
		select {
		case limiter <- struct{}{}:
			dlog.Server.Info("Server limit OK now, processing file", len(limiter), path)
		case <-ctx.Done():
			return
		}
	}

	// Check if we should use the turbo boost optimizations
	turboBoostEnabled := config.Env("DTAIL_TURBOBOOST_ENABLE")
	// Enable turbo boost for cat/grep/tail modes, but NOT for aggregate (MapReduce) operations
	// MapReduce requires the traditional channel-based approach to work correctly
	if turboBoostEnabled && r.server.aggregate == nil &&
		(r.mode == omode.CatClient || r.mode == omode.GrepClient || r.mode == omode.TailClient) {
		r.readWithTurboProcessor(ctx, ltx, path, globID, re, reader)
		return
	}

	// Original channel-based implementation
	lines := r.server.lines
	aggregate := r.server.aggregate

	for {
		if aggregate != nil {
			// Use a larger buffer for aggregate operations to handle high concurrency
			// This prevents deadlock when processing many files simultaneously
			lines = make(chan *line.Line, 10000)
			aggregate.NextLinesCh <- lines
		}
		if err := reader.Start(ctx, ltx, lines, re); err != nil {
			dlog.Server.Error(r.server.user, path, globID, err)
		}
		if aggregate != nil {
			// Also makes aggregate to Flush
			close(lines)
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

func (r *readCommand) readWithProcessor(ctx context.Context, ltx lcontext.LContext,
	path, globID string, re regex.Regex, reader fs.FileReader) {

	dlog.Server.Info(r.server.user, "Using channel-less grep implementation", path, globID)

	// Use the existing lines channel but with the processor-based reader
	lines := r.server.lines
	aggregate := r.server.aggregate

	// Use the optimized version if turbo boost is enabled
	turboBoostEnabled := config.Env("DTAIL_TURBOBOOST_ENABLE")

	for {
		if aggregate != nil {
			// Use a larger buffer for aggregate operations to handle high concurrency
			// This prevents deadlock when processing many files simultaneously
			lines = make(chan *line.Line, 10000)
			aggregate.NextLinesCh <- lines
		}

		// Create a processor that sends to the lines channel
		processor := NewChannellessLineProcessor(lines, globID)
		defer processor.Close()

		var err error
		if turboBoostEnabled {
			err = reader.StartWithProcessorOptimized(ctx, ltx, processor, re)
		} else {
			err = reader.StartWithProcessor(ctx, ltx, processor, re)
		}

		if err != nil {
			dlog.Server.Error(r.server.user, path, globID, err)
		}

		if aggregate != nil {
			// Also makes aggregate to Flush
			close(lines)
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

func (r *readCommand) readWithTurboProcessor(ctx context.Context, ltx lcontext.LContext,
	path, globID string, re regex.Regex, reader fs.FileReader) {

	dlog.Server.Info(r.server.user, "Using turbo channel-less implementation", path, globID)

	// Enable turbo mode if not already enabled
	if !r.server.IsTurboMode() {
		r.server.EnableTurboMode()
	}

	// Create a direct writer based on the mode
	// In serverless mode, write directly to stdout
	// In server mode, use the turbo channel
	var writer TurboWriter
	if r.server.serverless {
		// In serverless mode, write directly to stdout
		writer = NewDirectTurboWriter(os.Stdout, r.server.hostname, r.server.plain, r.server.serverless)
	} else {
		// In server mode, use the network writer with turbo channels
		writer = &TurboNetworkWriter{
			handler:    &r.server.baseHandler,
			hostname:   r.server.hostname,
			plain:      r.server.plain,
			serverless: r.server.serverless,
		}
	}

	for {
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> starting read loop iteration")
		
		// Create a direct processor that writes without channels
		processor := NewDirectLineProcessor(writer, globID)

		// Use the optimized reader
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> reader.StartWithPocessorOptimized -> about to start")
		err := reader.StartWithProcessorOptimized(ctx, ltx, processor, re)
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> reader.StartWithPocessorOptimized -> completed")
		if err != nil {
			dlog.Server.Error(r.server.user, path, globID, err)
		}

		// Ensure we flush before closing
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> flushing processor")
		if flushErr := processor.Flush(); flushErr != nil {
			dlog.Server.Error(r.server.user, path, globID, "flush error", flushErr)
		}

		// Close the processor after each iteration
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> closing processor")
		processor.Close()
		dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> processor closed")
		
		// Give time for data to be transmitted
		// This is crucial for integration tests to ensure all data is sent
		if !r.server.serverless {
			dlog.Server.Trace(r.server.user, path, globID, "readWithTurboProcessor -> waiting for data transmission")
			time.Sleep(50 * time.Millisecond)
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
