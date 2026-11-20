//go:build !(darwin || dragonfly || freebsd || netbsd || openbsd || aix || linux || solaris)

package clients

// flushTTYInput does nothing where the terminal's input queue can not be
// discarded with the ioctls of ttyflush_sysv.go and ttyflush_bsd.go.
func flushTTYInput(int) error { return nil }
