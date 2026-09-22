//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package client

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile takes an exclusive flock(2) on fd without blocking. It reports
// false and no error when another open file description holds the lock.
// flock locks belong to the open file description, so two separately opened
// descriptors exclude each other both across processes and within one
// process. Closing fd releases the lock.
func tryLockFile(fd *os.File) (bool, error) {
	err := unix.Flock(int(fd.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EINTR):
		return false, nil
	default:
		return false, err
	}
}
