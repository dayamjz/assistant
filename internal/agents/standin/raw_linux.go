package standin

import "golang.org/x/sys/unix"

// The requests that read and set a terminal's attributes on Linux.
const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)
