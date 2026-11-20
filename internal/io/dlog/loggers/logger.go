package loggers

import (
	"context"
	"io"
	"sync"
)

// Logger is there to plug in your own log implementation.
//
// The contract deliberately carries no timestamp: Raw and RawWithColors run once
// per retrieved payload line, and reading the clock there dominated client CPU
// on hosts whose clocksource the vDSO cannot read, such as hpet. Sinks that
// need wall time (the daily file sink) read and cache it themselves at a coarse
// granularity.
type Logger interface {
	Log(message string)
	LogWithColors(message, messageWithColors string)
	Raw(message string)
	RawWithColors(message, messageWithColors string)
	Flush()
	SupportsColors() bool
}

// RawBytesWriter is implemented by sinks that can write payload from a byte
// slice without first converting it to a string. RawBytes writes message
// verbatim, exactly like Raw. The slice is only valid during the call and must
// not be retained. Sinks without this capability receive a string via Raw.
type RawBytesWriter interface {
	RawBytes(message []byte)
}

// WriteRawBytes writes payload through logger's RawBytesWriter capability when
// it has one and through its string Raw method otherwise, so both paths produce
// identical output. It is the single dispatch point for sinks in this package
// and for DLog. message is not retained.
func WriteRawBytes(logger Logger, message []byte) {
	if writer, ok := logger.(RawBytesWriter); ok {
		writer.RawBytes(message)
		return
	}
	logger.Raw(string(message))
}

// Starter is implemented by loggers that own background work.
type Starter interface {
	Start(ctx context.Context, wg *sync.WaitGroup)
}

// Pauser is implemented by terminal loggers whose output must pause while an
// interactive prompt temporarily owns the terminal.
type Pauser interface {
	Pause()
	Resume()
}

// OutputRedirector is implemented by terminal loggers whose output can be
// passed through another writer for a while: the line editor of an
// interactive client, which draws the line being edited again below what
// is logged. A nil writer gives the logger its own output back.
type OutputRedirector interface {
	RedirectOutput(w io.Writer)
}

// OutputHolder is implemented by terminal loggers that can hold their
// output while they are paused: what is logged then is offered to take,
// which reports whether it took the text p (it copies it) to write it to w
// later, unchanged and in order, before the pause ends; the caller of the
// log method then does not wait for the pause. Text take refuses waits as
// without a hold. A nil take ends the holding. The line editor of an
// interactive client uses it while a Tab waits for a server's answer,
// which the connection handlers read only while they can log.
type OutputHolder interface {
	HoldOutput(take func(w io.Writer, p []byte) bool)
}

// Rotator is implemented by loggers backed by rotatable resources.
type Rotator interface {
	Rotate()
}
