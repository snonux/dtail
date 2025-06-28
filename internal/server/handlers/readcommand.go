package handlers

import (
	"context"
	"fmt"
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
	dlog.Server.Info(r.server.user, "Turbo boost check: enabled=", turboBoostEnabled, "mode=", r.mode)
	// Only enable channel-less for server mode, not serverless mode
	// Use the serverless field directly as it's more reliable
	if turboBoostEnabled && (r.mode == omode.CatClient || r.mode == omode.GrepClient) && !r.server.serverless {
		// Log to stderr for testing verification - only in server mode
		fmt.Fprintf(os.Stderr, "[DTAIL] Turbo boost enabled: using channel-less implementation for %s\n", path)
		r.readWithProcessor(ctx, ltx, path, globID, re, reader)
		return
	}

	// Original channel-based implementation
	lines := r.server.lines
	aggregate := r.server.aggregate

	for {
		if aggregate != nil {
			lines = make(chan *line.Line, 100)
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
	
	// Log to stderr for testing verification - only in server mode
	if !r.server.serverless {
		if turboBoostEnabled {
			fmt.Fprintf(os.Stderr, "[DTAIL] Turbo boost enabled: using optimized reader for %s\n", path)
		} else {
			fmt.Fprintf(os.Stderr, "[DTAIL] Using standard processor reader for %s\n", path)
		}
	}

	for {
		if aggregate != nil {
			lines = make(chan *line.Line, 100)
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
