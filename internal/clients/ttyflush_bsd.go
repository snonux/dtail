//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package clients

import "golang.org/x/sys/unix"

// fread is FREAD of <sys/fcntl.h>, the same on every BSD and macOS;
// golang.org/x/sys/unix does not define it.
const fread = 0x1

// flushTTYInput discards the input the terminal fd holds unread (tcflush
// with TCIFLUSH, which is the TIOCFLUSH ioctl with FREAD here).
func flushTTYInput(fd int) error {
	return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, fread)
}
