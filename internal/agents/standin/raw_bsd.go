//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package standin

import "golang.org/x/sys/unix"

// The requests that read and set a terminal's attributes on macOS and the
// BSDs.
const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)
