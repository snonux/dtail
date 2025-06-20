package handlers

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/constants"
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
	var queryStr string
	
	// Parse regex for non-MapReduce operations or when no aggregate exists
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

	dlog.Server.Debug("Processing mode:", r.mode)
	r.start(ctx, ltx, args, re, retries, queryStr)
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

// start implements direct processing for better performance
func (r *readCommand) start(ctx context.Context, ltx lcontext.LContext,
	args []string, re regex.Regex, retries int, queryStr string) {

	// Handle stdin input in serverless mode
	if (args[1] == "" || args[1] == "-") && r.isInputFromPipe() {
		dlog.Server.Debug("Reading data from stdin pipe")
		r.readStdin(ctx, ltx, re, queryStr)
		return
	}

	dlog.Server.Debug("Reading data from file(s)")
	r.readGlob(ctx, ltx, args[1], re, retries, queryStr)
}

// readGlob processes files using direct processing
func (r *readCommand) readGlob(ctx context.Context, ltx lcontext.LContext,
	glob string, re regex.Regex, retries int, queryStr string) {

	retryInterval := constants.ReadCommandRetryInterval
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

		r.readFiles(ctx, ltx, paths, glob, re, queryStr)
		return
	}

	r.server.sendln(r.server.serverMessages, dlog.Server.Warn(r.server.user,
		"Giving up to read file(s)"))
}

// readFiles processes multiple files using direct processing
func (r *readCommand) readFiles(ctx context.Context, ltx lcontext.LContext,
	paths []string, glob string, re regex.Regex, queryStr string) {

	// Choose output writer based on server mode
	var output io.Writer
	if r.server.serverless {
		// In serverless mode, write directly to stdout
		output = os.Stdout
	} else {
		// In client-server mode, write to server handler lines channel
		output = NewServerHandlerWriter(r.server, r.server.serverMessages, r.server.user)
		
	}

	// Create appropriate processor based on mode
	processor, needsFollowing := r.createProcessor(re, ltx, output, queryStr)

	// Process each file
	for _, path := range paths {
		// Generate globID just like the original system
		globID := r.makeGlobID(path, glob)
		
		
		if !r.server.user.HasFilePermission(path, "readfiles") {
			dlog.Server.Error(r.server.user, "No permission to read file", path)
			r.server.sendln(r.server.serverMessages, dlog.Server.Warn(r.server.user,
				"Unable to read file(s), check server logs"))
			continue
		}

		dlog.Server.Info(r.server.user, "Start reading", path)
		
		// Handle file following for tail operations
		if needsFollowing {
			// For aggregate processors, we need to use following with the aggregate processor
			if aggregateProcessor, ok := processor.(*fs.AggregateLineProcessor); ok {
				// Create a DirectProcessor wrapper that supports following
				directProcessor := fs.NewDirectProcessor(aggregateProcessor, output, globID, ltx)
				if err := directProcessor.ProcessFileWithTailing(ctx, path); err != nil {
					dlog.Server.Error(r.server.user, path, err)
					r.server.sendln(r.server.serverMessages, dlog.Server.Error(r.server.user,
						"Error processing file", path, err))
				}
			} else if tailProcessor, ok := processor.(*fs.TailProcessor); ok {
				followingProcessor := fs.NewFollowingTailProcessor(tailProcessor, output, globID, ltx)
				if err := followingProcessor.ProcessFileWithFollowing(ctx, path); err != nil {
					dlog.Server.Error(r.server.user, path, err)
					r.server.sendln(r.server.serverMessages, dlog.Server.Error(r.server.user,
						"Error processing file", path, err))
				}
			} else {
				// Fallback to regular processing
				directProcessor := fs.NewDirectProcessor(processor, output, globID, ltx)
				if err := directProcessor.ProcessFile(ctx, path); err != nil {
					dlog.Server.Error(r.server.user, path, err)
					r.server.sendln(r.server.serverMessages, dlog.Server.Error(r.server.user,
						"Error processing file", path, err))
				}
			}
		} else {
			// Regular file processing
			directProcessor := fs.NewDirectProcessor(processor, output, globID, ltx)
			if err := directProcessor.ProcessFile(ctx, path); err != nil {
				dlog.Server.Error(r.server.user, path, err)
				r.server.sendln(r.server.serverMessages, dlog.Server.Error(r.server.user,
					"Error processing file", path, err))
			}
		}
	}
}

// readStdin processes stdin using direct processing
func (r *readCommand) readStdin(ctx context.Context, ltx lcontext.LContext, re regex.Regex, queryStr string) {
	// Choose output writer based on server mode
	var output io.Writer
	if r.server.serverless {
		// In serverless mode, write directly to stdout
		output = os.Stdout
	} else {
		// In client-server mode, write to server handler lines channel
		output = NewServerHandlerWriter(r.server, r.server.serverMessages, r.server.user)
	}

	// Create appropriate processor based on mode
	processor, _ := r.createProcessor(re, ltx, output, queryStr)
	
	// Create direct processor with "-" as globID for stdin
	directProcessor := fs.NewDirectProcessor(processor, output, "-", ltx)

	dlog.Server.Info(r.server.user, "Start reading from stdin")
	
	if err := directProcessor.ProcessReader(ctx, os.Stdin, "-"); err != nil {
		dlog.Server.Error(r.server.user, "stdin", err)
		r.server.sendln(r.server.serverMessages, dlog.Server.Error(r.server.user,
			"Error processing stdin", err))
	}
}

// isMapReduceCommand checks if this is a command that's part of a MapReduce operation
func (r *readCommand) isMapReduceCommand(re regex.Regex) bool {
	// Only cat and tail commands can be part of MapReduce operations
	if r.mode != omode.CatClient && r.mode != omode.TailClient {
		return false
	}
	
	// Check if the regex contains MAPREDUCE pattern OR if it's a noop regex
	// (noop regex is used for CSV logformat in MapReduce operations)
	pattern := re.String()
	return strings.Contains(pattern, "MAPREDUCE:") || re.IsNoop()
}

// createProcessor creates the appropriate processor based on command mode
func (r *readCommand) createProcessor(re regex.Regex, ltx lcontext.LContext, output io.Writer, queryStr string) (fs.LineProcessor, bool) {
	hostname := r.server.hostname // Use server hostname
	plain := r.server.plain       // Use actual plain mode from server
	noColor := false              // Enable colors by default
	
	dlog.Server.Debug(r.server.user, "createProcessor: plain mode is", plain)
	
	// If there's an existing aggregate (from a 'map' command), we need to feed data to it
	// Create a lines channel and connect it to the aggregate
	if r.server.aggregate != nil {
		dlog.Server.Debug("Using existing aggregate, creating bridge processor")
		// Create a lines channel for the aggregate with larger buffer
		linesCh := make(chan *line.Line, 10000)
		// Connect the lines channel to the aggregate
		go func() {
			r.server.aggregate.NextLinesCh <- linesCh
		}()
		
		// Create a bridge processor that feeds lines to the aggregate
		var bridgeProcessor fs.LineProcessor
		if r.mode == omode.TailClient {
			bridgeProcessor = fs.NewAggregateLineProcessorForTail(linesCh, re, hostname, ltx)
		} else {
			bridgeProcessor = fs.NewAggregateLineProcessor(linesCh, re, hostname, ltx)
		}
		
		// Determine if following is needed
		needsFollowing := r.mode == omode.TailClient
		return bridgeProcessor, needsFollowing
	}
	
	// No existing aggregate - check if this is a standalone MapReduce operation
	isMapReduce := r.isMapReduceCommand(re) || r.mode == omode.MapClient
	
	switch r.mode {
	case omode.GrepClient:
		if isMapReduce && queryStr != "" {
			// This is a standalone MapReduce grep operation
			mapProcessor, err := fs.NewMapProcessor(plain, hostname, queryStr, output)
			if err != nil {
				dlog.Server.Error(r.server.user, "Failed to create MapReduce processor", err)
				return fs.NewGrepProcessor(re, plain, noColor, hostname, ltx.BeforeContext, ltx.AfterContext, ltx.MaxCount), false
			}
			return mapProcessor, false
		}
		return fs.NewGrepProcessor(re, plain, noColor, hostname, ltx.BeforeContext, ltx.AfterContext, ltx.MaxCount), false
	case omode.CatClient:
		if isMapReduce && queryStr != "" {
			// This is a standalone MapReduce cat operation
			mapProcessor, err := fs.NewMapProcessor(plain, hostname, queryStr, output)
			if err != nil {
				dlog.Server.Error(r.server.user, "Failed to create MapReduce processor", err)
				return fs.NewCatProcessor(plain, noColor, hostname), false
			}
			return mapProcessor, false
		}
		return fs.NewCatProcessor(plain, noColor, hostname), false
	case omode.TailClient:
		if isMapReduce && queryStr != "" {
			// This is a standalone MapReduce tail operation
			mapProcessor, err := fs.NewMapProcessor(plain, hostname, queryStr, output)
			if err != nil {
				dlog.Server.Error(r.server.user, "Failed to create MapReduce processor", err)
				return fs.NewTailProcessor(re, plain, noColor, hostname, true, true, 0), true
			}
			return mapProcessor, false
		}
		// Regular tail operation
		return fs.NewTailProcessor(re, plain, noColor, hostname, true, true, 0), true
	case omode.MapClient:
		// Direct MapReduce client - should have queryStr
		if queryStr != "" {
			mapProcessor, err := fs.NewMapProcessor(plain, hostname, queryStr, output)
			if err != nil {
				dlog.Server.Error(r.server.user, "Failed to create MapReduce processor", err)
				return fs.NewGrepProcessor(re, plain, noColor, hostname, ltx.BeforeContext, ltx.AfterContext, ltx.MaxCount), false
			}
			return mapProcessor, false
		}
		// Fallback
		return fs.NewGrepProcessor(re, plain, noColor, hostname, ltx.BeforeContext, ltx.AfterContext, ltx.MaxCount), false
	default:
		// Default to grep behavior
		return fs.NewGrepProcessor(re, plain, noColor, hostname, ltx.BeforeContext, ltx.AfterContext, ltx.MaxCount), false
	}
}
