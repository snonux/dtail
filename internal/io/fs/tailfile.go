package fs

import "github.com/mimecast/dtail/internal/logging"

// TailFile is to tail and filter a log file.
type TailFile struct {
	readFile
}

// NewTailFile returns a new file tailer.
func NewTailFile(filePath string, globID string, serverMessages chan<- string,
	maxLineLength int, logger logging.Logger) TailFile {

	return TailFile{
		readFile: readFile{
			logger:         logging.OrNop(logger),
			filePath:       filePath,
			globID:         globID,
			serverMessages: serverMessages,
			retry:          true,
			canSkipLines:   true,
			follow:         true,
			seekInitialEOF: true,
			maxLineLength:  maxLineLength,
		},
	}
}

// NewValidatedTailFile returns a new file tailer backed by a rooted open target.
func NewValidatedTailFile(filePath string, target ValidatedReadTarget, globID string,
	serverMessages chan<- string, maxLineLength int, logger logging.Logger) TailFile {

	tail := NewTailFile(filePath, globID, serverMessages, maxLineLength, logger)
	tail.validatedTarget = &target
	return tail
}
