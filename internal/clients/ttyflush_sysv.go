//go:build aix || linux || solaris

package clients

import "golang.org/x/sys/unix"

// flushTTYInput discards the input the terminal fd holds unread (tcflush
// with TCIFLUSH).
func flushTTYInput(fd int) error {
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}
