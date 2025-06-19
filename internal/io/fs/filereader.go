// Package fs provides file system operations and processors for DTail server-side
// log file handling. This package implements various file processing strategies
// including tailing, reading, grepping, and MapReduce operations with efficient
// streaming and resource management.
//
// Key components:
// - FileReader interface for abstracted file operations
// - Processors for different operation types (tail, cat, grep, mapr)
// - Chunked reading for efficient large file processing
// - Permission checking with Linux ACL support (optional)
// - Statistics tracking for file operations
//
// The package supports both one-time operations (cat, grep, mapr) and continuous
// monitoring (tail) with proper resource cleanup and error handling. All processors
// implement streaming output to minimize memory usage for large files.
package fs

import (
	"context"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

// FileReader defines the interface for all file processing operations on the
// DTail server. This interface abstracts different file processing strategies
// (tail, cat, grep, mapr) providing a uniform way to handle various log file
// operations with context management and streaming output.
type FileReader interface {
	// Start begins the file processing operation, streaming processed lines
	// to the output channel. The operation respects context cancellation
	// and applies regex filtering as specified.
	//
	// Parameters:
	//   ctx: Context for cancellation and timeout control
	//   ltx: Line context for before/after context lines and match limits
	//   lines: Output channel for processed log lines
	//   re: Compiled regex for line filtering (may be no-op for some operations)
	//
	// Returns:
	//   error: Any error encountered during file processing
	Start(ctx context.Context, ltx lcontext.LContext, lines chan<- *line.Line,
		re regex.Regex) error
	
	// FilePath returns the absolute path of the file being processed.
	// This is used for logging, statistics, and client identification.
	FilePath() string
	
	// Retry indicates whether this file operation should be retried
	// if it fails. Typically true for tail operations (long-running)
	// and false for one-time operations (cat, grep, mapr).
	Retry() bool
}
