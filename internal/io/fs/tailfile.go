package fs

// TailFile is to tail and filter a log file.
type TailFile struct {
	readFile
}

// NewTailFile returns a new file tailer.
func NewTailFile(filePath string, globID string, serverMessages chan<- string,
	maxLineLength int) TailFile {

	return TailFile{
		readFile: readFile{
			filePath:       filePath,
			globID:         globID,
			serverMessages: serverMessages,
			retry:          true,
			canSkipLines:   true,
			seekEOF:        true,
			maxLineLength:  maxLineLength,
		},
	}
}

// NewValidatedTailFile returns a new file tailer backed by a rooted open target.
func NewValidatedTailFile(filePath string, target ValidatedReadTarget, globID string,
	serverMessages chan<- string, maxLineLength int) TailFile {

	tail := NewTailFile(filePath, globID, serverMessages, maxLineLength)
	tail.readFile.validatedTarget = &target
	return tail
}
