package fs

import "github.com/mimecast/dtail/internal/logging"

// CatFile is for reading a whole file.
type CatFile struct {
	readFile
}

// NewCatFile returns a new file catter.
func NewCatFile(filePath string, globID string, serverMessages chan<- string,
	maxLineLength int, logger logging.Logger) CatFile {

	return CatFile{
		readFile: readFile{
			logger:         logging.OrNop(logger),
			filePath:       filePath,
			globID:         globID,
			serverMessages: serverMessages,
			retry:          false,
			canSkipLines:   false,
			follow:         false,
			seekInitialEOF: false,
			maxLineLength:  maxLineLength,
		},
	}
}

// NewValidatedCatFile returns a new file catter backed by a rooted open target.
func NewValidatedCatFile(filePath string, target ValidatedReadTarget, globID string,
	serverMessages chan<- string, maxLineLength int, logger logging.Logger) CatFile {

	cat := NewCatFile(filePath, globID, serverMessages, maxLineLength, logger)
	cat.validatedTarget = &target
	return cat
}
