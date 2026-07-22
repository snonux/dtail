package fs

import (
	"context"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

// FileReader is the interface used on the dtail server to read/cat/grep/mapr...
// a file. Line delivery is processor-based (line.Processor); the historic
// channel-based Start(chan<- *line.Line) method was removed once every read path
// migrated to the processor pipeline (task iv0).
type FileReader interface {
	StartWithProcessor(ctx context.Context, ltx lcontext.LContext, processor line.Processor,
		re regex.Regex) error
	StartWithProcessorOptimized(ctx context.Context, ltx lcontext.LContext, processor line.Processor,
		re regex.Regex) error
	FilePath() string
	Retry() bool
}
