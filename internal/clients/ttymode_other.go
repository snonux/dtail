//go:build !(darwin || dragonfly || freebsd || netbsd || openbsd || aix || linux || solaris || zos)

package clients

import (
	"errors"
	"os"
)

// enterCbreakMode is not supported here: the interactive control loop then
// reads whole lines without the line editor (no completion or history).
func enterCbreakMode(int) (ttyMode, error) {
	return nil, errors.New("terminal line editing is not supported on this platform")
}

// controllingTerminal is not known here: terminals are the same only as
// the same file (sameTerminal).
func controllingTerminal(*os.File) bool { return false }

// watchTTYSignals has nothing to watch without the line editor.
func watchTTYSignals(func(), func()) func() { return func() {} }
