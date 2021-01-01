package logger

import "time"

// Helper type to make logging non-blocking.
type buf struct {
	time    time.Time
	message string
}
